package worker

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ponytojas/darsena/internal/platform/jobs"
	rt "github.com/ponytojas/darsena/internal/platform/runtime"
	"github.com/ponytojas/darsena/internal/platform/runtime/intent"
	"github.com/ponytojas/darsena/internal/platform/runtime/reconcile"
)

// GuardIntentDB records the path of a runtime intention database that this
// process does not open. While the runtime is disabled, an existing file there
// makes image edits and deletions fail closed with runtime_state_unknown.
func (s *Service) GuardIntentDB(path string) { s.intentGuard = path }

// runtimeIntents reports whether any runtime intention exists for the
// application. known is false when that cannot be determined. With the runtime
// enabled callers must hold its gate, so no intention can appear meanwhile.
// Without it, an intention database cannot have been written by this process:
// the answer is "none" unless a guard path names a database that exists or
// cannot be inspected.
func (s *Service) runtimeIntents(ctx context.Context, app jobs.Application) (exists, known bool) {
	// The persistent mark is written before any dispatch, in every mode.
	if app.ServiceUsed {
		return true, true
	}
	if s.runtime != nil {
		has, err := s.runtime.store.HasService(ctx, app.ID)
		return has, err == nil
	}
	if s.intentGuard == "" {
		return false, true
	}
	if _, err := os.Lstat(s.intentGuard); errors.Is(err, os.ErrNotExist) {
		return false, true
	}
	return false, false
}

// acquireRuntime takes the runtime gate without waiting. It answers the request
// itself and returns false when the gate is busy.
func (s *Service) acquireRuntime(w http.ResponseWriter) bool {
	if s.runtime == nil {
		return true
	}
	if !s.runtime.tryAcquire() {
		failureCode(w, http.StatusConflict, "runtime_busy", "runtime operation busy")
		return false
	}
	return true
}
func (s *Service) releaseRuntime() {
	if s.runtime != nil {
		s.runtime.release()
	}
}

// resolveAccess merges the access fields of an update over the stored ones.
// It returns nil when the request names none (keep everything) and whether the
// result differs from what is stored. Leaving local clears the domain; entering
// local without one defaults it to the name, which the store rejects if invalid.
func resolveAccess(current jobs.Application, access *string, shared *bool, domain *string) (*jobs.AppAccess, bool) {
	if access == nil && shared == nil && domain == nil {
		return nil, false
	}
	cur := jobs.AppAccess{Access: current.Access, SharedNetwork: current.SharedNetwork}
	if current.Domain != nil {
		cur.Domain = *current.Domain
	}
	next := cur
	if access != nil {
		next.Access = *access
		if next.Access != jobs.AccessLocal {
			next.Domain = ""
		}
		if next.Access == jobs.AccessIsolated {
			next.SharedNetwork = false
		}
	}
	if shared != nil {
		next.SharedNetwork = *shared
	}
	if domain != nil {
		next.Domain = *domain
	}
	if next.Access == jobs.AccessLocal && next.Domain == "" {
		next.Domain = current.Name
	}
	changed := next.Access != cur.Access || next.SharedNetwork != cur.SharedNetwork || strings.ToLower(next.Domain) != cur.Domain
	return &next, changed
}

func (s *Service) updateApplication(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Version       *int    `json:"version"`
		Image         *string `json:"image"`
		Port          *int    `json:"port"`
		Access        *string `json:"access"`
		SharedNetwork *bool   `json:"shared_network"`
		Domain        *string `json:"domain"`
	}
	if !configBody(w, r, &in) {
		return
	}
	if in.Version == nil || in.Image == nil || in.Port == nil {
		failureCode(w, 400, "invalid_application", "version, image and port are required")
		return
	}
	image, ok := pinnedImage(w, *in.Image)
	if !ok {
		return
	}
	in.Image = &image
	id := r.PathValue("id")
	current, err := s.store.Application(r.Context(), id)
	if errors.Is(err, jobs.ErrNotFound) {
		failureCode(w, 404, "application_not_found", "application not found")
		return
	}
	if err != nil {
		failure(w, 500, "unable to read application")
		return
	}
	access, accessChanged := resolveAccess(current, in.Access, in.SharedNetwork, in.Domain)
	if *in.Image != current.Image || accessChanged || (*in.Port != current.Port && (current.Access != jobs.AccessIsolated || (access != nil && access.Access != jobs.AccessIsolated))) {
		// The service identity is fixed per application, so once any intention
		// exists the image and the network settings can no longer change. The
		// gate keeps one from appearing between this check and the update.
		if !s.acquireRuntime(w) {
			return
		}
		defer s.releaseRuntime()
		// Re-read: a service request may have marked the application meanwhile.
		current, err = s.store.Application(r.Context(), id)
		if errors.Is(err, jobs.ErrNotFound) {
			failureCode(w, 404, "application_not_found", "application not found")
			return
		}
		if err != nil {
			failure(w, 500, "unable to read application")
			return
		}
		access, accessChanged = resolveAccess(current, in.Access, in.SharedNetwork, in.Domain)
		portLocked := *in.Port != current.Port && (current.Access != jobs.AccessIsolated || (access != nil && access.Access != jobs.AccessIsolated))
		if *in.Image != current.Image || accessChanged || portLocked {
			exists, known := s.runtimeIntents(r.Context(), current)
			if !known {
				failureCode(w, 409, "runtime_state_unknown", "runtime state cannot be determined")
				return
			}
			if exists && *in.Image != current.Image {
				failureCode(w, 409, "image_locked", "the image cannot change after a service was requested")
				return
			}
			if exists {
				failureCode(w, 409, "access_locked", "access, network, domain and port cannot change after a service was requested")
				return
			}
		}
	}
	app, err := s.store.UpdateApplication(r.Context(), id, *in.Version, *in.Image, *in.Port, access)
	switch {
	case accessFailure(w, err):
	case errors.Is(err, jobs.ErrInvalid):
		failureCode(w, 400, "invalid_application", "invalid application")
	case errors.Is(err, jobs.ErrNotFound):
		failureCode(w, 404, "application_not_found", "application not found")
	case errors.Is(err, jobs.ErrConflict):
		failureCode(w, 409, "update_conflict", "application changed; reload before saving")
	case err != nil:
		failure(w, 500, "unable to update application")
	default:
		send(w, 200, app)
	}
}

// serviceGone reports whether an application that once requested a service no
// longer has a container, so it can be deleted; otherwise it writes the reason.
// The caller holds the runtime gate, so nothing can be dispatched meanwhile.
// Intention records are kept: application ids are random and never reused.
func (s *Service) serviceGone(w http.ResponseWriter, r *http.Request, appID string) bool {
	if s.runtime == nil {
		failureCode(w, 409, "runtime_state_unknown", "the runtime is needed to confirm the service no longer exists")
		return false
	}
	id := rt.Identity{Installation: s.runtime.installation, Service: appID, Operation: serviceOperation}
	item, err := s.runtime.store.Get(r.Context(), id)
	if errors.Is(err, intent.ErrNotFound) {
		// Marked but nothing was ever persisted or sent.
		return true
	}
	if err != nil {
		failureCode(w, 409, "runtime_state_unknown", "runtime state cannot be determined")
		return false
	}
	if item.Phase == intent.Prepared {
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	switch reconcile.Observe(ctx, s.runtime.provider, item.Ref) {
	case reconcile.Absent:
		return true
	case reconcile.Running, reconcile.Created, reconcile.Stopped:
		failureCode(w, 409, "application_has_service", "remove the service before deleting the application")
	default:
		failureCode(w, 409, "runtime_state_unknown", "runtime state cannot be determined")
	}
	return false
}

func (s *Service) deleteApplication(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.acquireRuntime(w) {
		return
	}
	defer s.releaseRuntime()
	app, err := s.store.Application(r.Context(), id)
	if errors.Is(err, jobs.ErrNotFound) {
		failureCode(w, 404, "application_not_found", "application not found")
		return
	} else if err != nil {
		failure(w, 500, "unable to read application")
		return
	}
	exists, known := s.runtimeIntents(r.Context(), app)
	if !known {
		failureCode(w, 409, "runtime_state_unknown", "runtime state cannot be determined")
		return
	}
	if exists && !s.serviceGone(w, r, app.ID) {
		return
	}
	// Exclude configuration writers: with the write side held none can sit
	// between its application check and its write, so no revision can be
	// created after the purge. The purge comes first; if it fails the
	// definition stays.
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	if s.vault != nil {
		if err := s.vault.Purge(r.Context(), id); err != nil {
			failureCode(w, 500, "configuration_unavailable", "configuration unavailable")
			return
		}
	}
	err = s.store.DeleteApplication(r.Context(), id)
	if errors.Is(err, jobs.ErrNotFound) {
		failureCode(w, 404, "application_not_found", "application not found")
		return
	}
	if err != nil {
		failure(w, 500, "unable to delete application")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) assignApplications(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ProjectID      *string  `json:"project_id"`
		ApplicationIDs []string `json:"application_ids"`
		OnlyUnassigned bool     `json:"only_unassigned"`
	}
	if !configBody(w, r, &in) {
		return
	}
	if in.ApplicationIDs == nil {
		failureCode(w, 400, "invalid_application", "application_ids is required")
		return
	}
	changed, err := s.store.AssignApplications(r.Context(), in.ProjectID, in.ApplicationIDs, in.OnlyUnassigned)
	switch {
	case errors.Is(err, jobs.ErrProjectNotFound):
		failureCode(w, 404, "project_not_found", "project not found")
		return
	case errors.Is(err, jobs.ErrNotFound):
		failureCode(w, 404, "application_not_found", "application not found")
		return
	case errors.Is(err, jobs.ErrInvalid):
		failureCode(w, 400, "invalid_application", "invalid assignment")
		return
	case err != nil:
		failure(w, 500, "unable to assign applications")
		return
	}
	out := make([]jobs.Application, 0, len(changed))
	for _, id := range changed {
		app, err := s.store.Application(r.Context(), id)
		if err != nil {
			failure(w, 500, "unable to read application")
			return
		}
		out = append(out, app)
	}
	send(w, 200, map[string]any{"applications": out})
}

func (s *Service) listProjects(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.Projects(r.Context())
	if err != nil {
		failure(w, 500, "unable to list projects")
		return
	}
	send(w, 200, list)
}

func (s *Service) createProject(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	if !configBody(w, r, &in) {
		return
	}
	p, err := s.store.CreateProject(r.Context(), in.Name, in.Color)
	switch {
	case errors.Is(err, jobs.ErrInvalid):
		failureCode(w, 400, "invalid_project", "invalid project")
	case errors.Is(err, jobs.ErrConflict):
		failureCode(w, 409, "project_name_taken", "a project with that name already exists")
	case errors.Is(err, jobs.ErrProjectLimit):
		failureCode(w, 409, "project_limit", "project limit reached")
	case err != nil:
		failure(w, 500, "unable to create project")
	default:
		send(w, 201, p)
	}
}

func (s *Service) updateProject(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Version *int   `json:"version"`
		Name    string `json:"name"`
		Color   string `json:"color"`
	}
	if !configBody(w, r, &in) {
		return
	}
	if in.Version == nil {
		failureCode(w, 400, "invalid_project", "version is required")
		return
	}
	id := r.PathValue("id")
	p, err := s.store.UpdateProject(r.Context(), id, *in.Version, in.Name, in.Color)
	if errors.Is(err, jobs.ErrConflict) {
		// The store reports a stale version and a taken name alike.
		if cur, e := s.store.Project(r.Context(), id); e == nil && cur.Version == *in.Version {
			failureCode(w, 409, "project_name_taken", "a project with that name already exists")
		} else {
			failureCode(w, 409, "update_conflict", "project changed; reload before saving")
		}
		return
	}
	switch {
	case errors.Is(err, jobs.ErrInvalid):
		failureCode(w, 400, "invalid_project", "invalid project")
	case errors.Is(err, jobs.ErrProjectNotFound):
		failureCode(w, 404, "project_not_found", "project not found")
	case err != nil:
		failure(w, 500, "unable to update project")
	default:
		send(w, 200, p)
	}
}

func (s *Service) deleteProject(w http.ResponseWriter, r *http.Request) {
	p, ids, err := s.store.DeleteProject(r.Context(), r.PathValue("id"))
	switch {
	case errors.Is(err, jobs.ErrProjectNotFound):
		failureCode(w, 404, "project_not_found", "project not found")
	case err != nil:
		failure(w, 500, "unable to delete project")
	default:
		send(w, 200, map[string]any{"project": p, "application_ids": ids})
	}
}
