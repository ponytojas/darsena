package worker

import (
	"context"
	"errors"
	"math"
	"net/http"
	"time"

	"github.com/ponytojas/darsena/internal/platform/configvault"
	"github.com/ponytojas/darsena/internal/platform/jobs"
	rt "github.com/ponytojas/darsena/internal/platform/runtime"
	"github.com/ponytojas/darsena/internal/platform/runtime/execution"
	"github.com/ponytojas/darsena/internal/platform/runtime/intent"
	"github.com/ponytojas/darsena/internal/platform/runtime/reconcile"
)

// RuntimeProvider is only enabled explicitly before the worker starts. It must
// enforce its own immutable ownership and configuration checks.
type RuntimeProvider interface {
	execution.ConfiguredCreator
	reconcile.Inspector
	Stop(context.Context, rt.Ref) (rt.Result, error)
	Remove(context.Context, rt.Ref) error
	Check(context.Context) error
	// EnsureNetworks creates or verifies the networks a specification needs
	// without creating a container; a no-op for isolated specifications.
	EnsureNetworks(context.Context, rt.Spec) error
	ImageProvider
}

// serviceOperation is the fixed operation of every application's service identity.
const serviceOperation = "initial"

type realRuntime struct {
	installation string
	store        *intent.Store
	prepare      execution.PrepareRef
	provider     RuntimeProvider
	images       ImageProvider
	pulls        *imagePulls
	gate         chan struct{}
}

// tryAcquire takes the runtime gate without waiting; it reports false if busy.
func (r *realRuntime) tryAcquire() bool {
	select {
	case r.gate <- struct{}{}:
		return true
	default:
		return false
	}
}
func (r *realRuntime) release() { <-r.gate }

// EnableRuntime opts into experimental operations on the private worker socket.
// The caller must validate and protect the socket and close the service. The
// worker takes ownership of store only after this method succeeds.
func (s *Service) EnableRuntime(installation string, store *intent.Store, prepare execution.PrepareRef, provider RuntimeProvider) error {
	if s == nil || s.vault == nil || s.runtime != nil || store == nil || prepare == nil || provider == nil || !(rt.Identity{Installation: installation, Service: "probe", Operation: "initial"}).Valid() {
		return errors.New("invalid experimental runtime configuration")
	}
	s.runtime = &realRuntime{installation: installation, store: store, prepare: prepare, provider: provider, images: provider, pulls: newImagePulls(), gate: make(chan struct{}, 1)}
	s.mux.HandleFunc("GET /runtime/info", func(w http.ResponseWriter, r *http.Request) {
		send(w, http.StatusOK, map[string]string{"mode": "experimental", "installation": installation, "policy": "dockerlocal-v4"})
	})
	s.mux.HandleFunc("GET /runtime/ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if provider.Check(ctx) != nil || ctx.Err() != nil {
			send(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
			return
		}
		send(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	s.mux.HandleFunc("GET /routes/{domain}", s.route)
	s.mux.HandleFunc("POST /image/pull", s.pullImage)
	s.mux.HandleFunc("POST /applications/{id}/service", s.createService)
	s.mux.HandleFunc("GET /applications/{id}/service", s.getService)
	s.mux.HandleFunc("POST /applications/{id}/service/refresh", s.refreshService)
	s.mux.HandleFunc("POST /applications/{id}/service/stop", s.stopService)
	s.mux.HandleFunc("POST /applications/{id}/service/remove", s.removeService)
	return nil
}

type serviceResponse struct {
	ApplicationID string                `json:"application_id"`
	OperationID   string                `json:"operation_id"`
	Revision      uint64                `json:"revision"`
	Phase         intent.Phase          `json:"phase"`
	Observation   reconcile.Observation `json:"observation"`
	Version       uint64                `json:"version"`
	Action        *intent.Action        `json:"action,omitempty"`
	ExitCode      *int                  `json:"exit_code,omitempty"`
	// Access is the network access the service was requested with. Domain is
	// its name under .localhost, only for local access. The host port is never
	// part of this response: a link by port would send the owner cookie.
	Access string  `json:"access"`
	Domain *string `json:"domain"`
}

func (s *Service) serviceView(ctx context.Context, v intent.Intent) serviceResponse {
	view := serviceResponse{ApplicationID: v.Ref.Identity.Service, OperationID: v.Ref.Identity.Operation, Revision: v.Revision, Phase: v.Phase, Observation: v.Observation, Version: v.Version, Access: v.Ref.Access}
	if view.Access == "" {
		view.Access = rt.AccessIsolated
	}
	if view.Access == rt.AccessLocal {
		if app, err := s.store.Application(ctx, v.Ref.Identity.Service); err == nil {
			view.Domain = app.Domain
		}
	}
	if a, err := s.runtime.store.LastAction(ctx, v.Ref.Identity); err == nil {
		view.Action = &a
	} else if !errors.Is(err, intent.ErrNotFound) {
		view.Action = &intent.Action{Kind: "unknown", Phase: intent.Dispatched}
	}
	return view
}
func (s *Service) serviceIdentity(w http.ResponseWriter, r *http.Request) (rt.Identity, bool) {
	appID := r.PathValue("id")
	_, err := s.store.Application(r.Context(), appID)
	if errors.Is(err, jobs.ErrNotFound) {
		failure(w, 404, "application not found")
		return rt.Identity{}, false
	}
	if err != nil {
		failure(w, 500, "application unavailable")
		return rt.Identity{}, false
	}
	id := rt.Identity{Installation: s.runtime.installation, Service: appID, Operation: serviceOperation}
	if !id.Valid() {
		failure(w, 500, "application unavailable")
		return rt.Identity{}, false
	}
	return id, true
}
func (s *Service) createService(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Revision uint64 `json:"revision"`
	}
	if !configBody(w, r, &input) {
		return
	}
	// Revision 0 means "no saved configuration": an empty first revision is
	// saved before dispatching, so variables are not required.
	if input.Revision > math.MaxInt64 {
		failure(w, 400, "invalid revision")
		return
	}
	id, ok := s.serviceIdentity(w, r)
	if !ok {
		return
	}
	if s.beforeGate != nil {
		s.beforeGate()
	}
	select {
	case s.runtime.gate <- struct{}{}:
		defer func() { <-s.runtime.gate }()
	default:
		failure(w, 409, "runtime operation busy")
		return
	}
	app, err := s.store.Application(r.Context(), id.Service)
	if errors.Is(err, jobs.ErrNotFound) {
		// Deleted between the identity check and the gate.
		failureCode(w, 404, "application_not_found", "application not found")
		return
	}
	if err != nil {
		failure(w, 500, "application unavailable")
		return
	}
	// Definitions saved before images were normalized are canonicalized here.
	image, err := rt.ParseImage(app.Image)
	if err != nil || image.Digest == "" {
		failureCode(w, 400, "invalid_image", "the saved image is not a valid reference pinned by digest")
		return
	}
	spec := rt.Spec{Identity: id, Image: image.Pinned(), Revision: input.Revision}
	if input.Revision == 0 {
		spec.Revision = 1
	}
	// An isolated spec stays exactly what it always was: no access fields.
	if app.Access == jobs.AccessInternal || app.Access == jobs.AccessLocal {
		if app.Access == jobs.AccessLocal && app.Domain == nil {
			failureCode(w, 400, "invalid_domain", "a locally open application needs a domain")
			return
		}
		spec.Access, spec.Port, spec.Shared, spec.Alias = app.Access, app.Port, app.SharedNetwork, app.Name
		if app.ProjectID != nil {
			spec.Project = *app.ProjectID
		}
	}
	prepared, prepErr := s.runtime.prepare(spec)
	if prepErr != nil || spec.Validate() != nil {
		failureCode(w, 400, "invalid_image", "application is incompatible with the runtime")
		return
	}
	// An already-dispatched request is an immutable operation, not a reason to
	// re-read configuration or send a second create after a later edit.
	existing, existingErr := s.runtime.store.Get(r.Context(), id)
	if existingErr == nil {
		if existing.Revision != input.Revision || existing.Ref != prepared {
			failure(w, 409, "operation or configuration conflicts")
			return
		}
		if existing.Phase != intent.Prepared {
			send(w, http.StatusOK, s.serviceView(r.Context(), existing))
			return
		}
	} else if !errors.Is(existingErr, intent.ErrNotFound) {
		failure(w, 503, "operation unavailable")
		return
	}
	// Before anything is persisted, the image must be present and usable:
	// otherwise the single initial operation would be spent on a failure.
	if !s.imageReady(w, r, spec.Image) {
		return
	}
	// Networks come before the claim for the same reason: a daemon without
	// address space or with a foreign network must not spend the operation.
	if spec.Access != "" && !s.networksReady(w, r, spec) {
		return
	}
	if input.Revision == 0 {
		if _, err := s.vault.Initialize(r.Context(), id.Service); errors.Is(err, configvault.ErrConflict) {
			send(w, http.StatusConflict, map[string]string{"error": "configuration revision conflict", "code": "revision_conflict_before_dispatch"})
			return
		} else if err != nil {
			failure(w, 503, "configuration unavailable")
			return
		}
	}
	// Persistent mark first: if it cannot be written nothing is dispatched.
	// Deletion and image edits consult it even when this process later runs
	// without the runtime. A request that then fails before dispatching leaves
	// the mark set, which errs on the safe side.
	if !app.ServiceUsed {
		if err = s.store.MarkRuntimeUsed(r.Context(), app.ID); errors.Is(err, jobs.ErrNotFound) {
			failureCode(w, 404, "application_not_found", "application not found")
			return
		} else if err != nil {
			failure(w, 503, "operation unavailable")
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 75*time.Second)
	defer cancel()
	result, err := execution.CreateConfigured(ctx, s.runtime.store, s.runtime.prepare, s.runtime.provider, s.vault, spec)
	if errors.Is(err, execution.ErrAlreadyDispatched) {
		item, readErr := s.runtime.store.Get(ctx, id)
		if readErr != nil {
			failure(w, 503, "operation unavailable")
			return
		}
		send(w, http.StatusOK, s.serviceView(r.Context(), item))
		return
	}
	if errors.Is(err, execution.ErrRevisionConflict) {
		// This invocation did not claim a dispatch. Another concurrent request
		// could still have done so; clients must refresh state before retrying.
		send(w, http.StatusConflict, map[string]string{"error": "configuration revision conflict", "code": "revision_conflict_before_dispatch"})
		return
	}
	if errors.Is(err, execution.ErrConflict) {
		failure(w, 409, "operation or configuration conflicts")
		return
	}
	if errors.Is(err, execution.ErrInvalid) {
		failure(w, 400, "invalid service configuration")
		return
	}
	if result.Intent.Ref.Identity == id && result.Dispatched {
		send(w, http.StatusAccepted, s.serviceView(r.Context(), result.Intent))
		return
	}
	if err != nil {
		failure(w, 503, "operation unavailable")
		return
	}
	send(w, http.StatusCreated, s.serviceView(r.Context(), result.Intent))
}

// networksReady writes a coded failure unless the networks of a specification
// exist and are owned by this installation.
func (s *Service) networksReady(w http.ResponseWriter, r *http.Request, spec rt.Spec) bool {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	err := s.runtime.provider.EnsureNetworks(ctx, spec)
	switch {
	case err == nil:
		return true
	case errors.Is(err, rt.ErrNetworkUnavailable):
		failureCode(w, 503, "network_unavailable", "no network could be created; the daemon has no address space left")
	case errors.Is(err, rt.ErrConflict):
		failureCode(w, 409, "network_conflict", "a network with the expected name exists but is not owned by Dársena")
	default:
		failureCode(w, 503, "runtime_unavailable", "networks could not be prepared")
	}
	return false
}

// route answers the application proxy on the worker socket only. It maps an
// exact domain to the loopback port Docker assigned to the running service. The
// port is read by a plain inspection; no observation is recorded.
func (s *Service) route(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	if d, err := jobs.NormalizeDomain(domain); err != nil || d != domain {
		failureCode(w, 404, "route_not_found", "unknown domain")
		return
	}
	app, err := s.store.ApplicationByDomain(r.Context(), domain)
	if errors.Is(err, jobs.ErrNotFound) || (err == nil && app.Access != jobs.AccessLocal) {
		failureCode(w, 404, "route_not_found", "unknown domain")
		return
	}
	if err != nil {
		failureCode(w, 503, "route_unavailable", "route unavailable")
		return
	}
	item, err := s.runtime.store.Get(r.Context(), rt.Identity{Installation: s.runtime.installation, Service: app.ID, Operation: serviceOperation})
	if errors.Is(err, intent.ErrNotFound) {
		failureCode(w, 503, "not_running", "the service is not running")
		return
	}
	if err != nil {
		failureCode(w, 503, "route_unavailable", "route unavailable")
		return
	}
	if item.Ref.Access != rt.AccessLocal {
		failureCode(w, 404, "route_not_found", "unknown domain")
		return
	}
	if item.Phase == intent.Prepared {
		failureCode(w, 503, "not_running", "the service is not running")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	res, err := s.runtime.provider.Inspect(ctx, item.Ref)
	if errors.Is(err, rt.ErrNotFound) {
		failureCode(w, 503, "not_running", "the service is not running")
		return
	}
	if err != nil || ctx.Err() != nil || res.Ref != item.Ref {
		failureCode(w, 503, "route_unavailable", "route unavailable")
		return
	}
	if res.State != rt.StateRunning || res.HostPort < 1 || res.HostPort > 65535 {
		failureCode(w, 503, "not_running", "the service is not running")
		return
	}
	send(w, 200, map[string]int{"host_port": res.HostPort})
}

// imageReady writes a coded failure unless the pinned image is available.
func (s *Service) imageReady(w http.ResponseWriter, r *http.Request, pinned string) bool {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	status, err := s.runtime.images.ImageStatus(ctx, pinned)
	switch {
	case err != nil:
		failureCode(w, 503, "runtime_unavailable", "image availability could not be checked")
	case status.State == rt.ImageMissing:
		failureCode(w, 409, "image_missing", "the image is not present in the runtime; download it first")
	case status.State != rt.ImageAvailable:
		failureCode(w, 400, "image_incompatible", "the image cannot run in the isolated runtime")
	default:
		return true
	}
	return false
}

func (s *Service) getService(w http.ResponseWriter, r *http.Request) {
	id, ok := s.serviceIdentity(w, r)
	if !ok {
		return
	}
	item, err := s.runtime.store.Get(r.Context(), id)
	if errors.Is(err, intent.ErrNotFound) {
		failure(w, 404, "service not found")
		return
	}
	if err != nil {
		failure(w, 503, "operation unavailable")
		return
	}
	send(w, 200, s.serviceView(r.Context(), item))
}
func (s *Service) refreshService(w http.ResponseWriter, r *http.Request) {
	id, ok := s.serviceIdentity(w, r)
	if !ok {
		return
	}
	select {
	case s.runtime.gate <- struct{}{}:
		defer func() { <-s.runtime.gate }()
	default:
		failure(w, 409, "runtime operation busy")
		return
	}
	item, err := s.runtime.store.Get(r.Context(), id)
	if errors.Is(err, intent.ErrNotFound) {
		failure(w, 404, "service not found")
		return
	}
	if err != nil {
		failure(w, 503, "operation unavailable")
		return
	}
	if item.Phase == intent.Prepared {
		send(w, 200, s.serviceView(r.Context(), item))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	obs, exitCode := reconcile.ObserveExit(ctx, s.runtime.provider, item.Ref)
	if ctx.Err() != nil {
		obs = reconcile.Unknown
	}
	if r.Context().Err() != nil {
		return
	}
	action, actionErr := s.runtime.store.LastAction(r.Context(), id)
	if actionErr == nil {
		item, _, err = s.runtime.store.ObserveAction(r.Context(), id, action.Kind, action.Version, item.Version, obs)
	} else if errors.Is(actionErr, intent.ErrNotFound) {
		item, err = s.runtime.store.RecordObservation(r.Context(), id, item.Version, item.Phase, obs)
	} else {
		err = actionErr
	}
	if errors.Is(err, intent.ErrConflict) {
		failure(w, 409, "operation changed; refresh again")
		return
	}
	if err != nil {
		failure(w, 503, "operation unavailable")
		return
	}
	view := s.serviceView(r.Context(), item)
	if obs == reconcile.Stopped {
		// Only known right after an inspection; it is not persisted.
		view.ExitCode = exitCode
	}
	send(w, 200, view)
}

func (s *Service) stopService(w http.ResponseWriter, r *http.Request) { s.mutateService(w, r, "stop") }
func (s *Service) removeService(w http.ResponseWriter, r *http.Request) {
	s.mutateService(w, r, "remove")
}

func (s *Service) mutateService(w http.ResponseWriter, r *http.Request, kind string) {
	var input struct {
		Version           uint64 `json:"version"`
		AcknowledgeRemove bool   `json:"acknowledge_remove"`
	}
	if !configBody(w, r, &input) {
		return
	}
	if input.Version == 0 || input.Version >= math.MaxInt64 || (kind == "remove" && !input.AcknowledgeRemove) {
		failure(w, 400, "version and removal acknowledgement required")
		return
	}
	id, ok := s.serviceIdentity(w, r)
	if !ok {
		return
	}
	select {
	case s.runtime.gate <- struct{}{}:
		defer func() { <-s.runtime.gate }()
	default:
		failure(w, 409, "runtime operation busy")
		return
	}
	item, action, dispatch, err := s.runtime.store.DispatchAction(r.Context(), id, kind, input.Version)
	if errors.Is(err, intent.ErrNotFound) {
		failure(w, 404, "service not found")
		return
	}
	if errors.Is(err, intent.ErrConflict) {
		failure(w, 409, "operation changed or service is not in the required state")
		return
	}
	if err != nil {
		failure(w, 503, "operation unavailable")
		return
	}
	if !dispatch {
		send(w, 200, s.serviceView(r.Context(), item))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 75*time.Second)
	defer cancel()
	obs := reconcile.Unknown
	if kind == "stop" {
		result, e := s.runtime.provider.Stop(ctx, item.Ref)
		if e == nil && ctx.Err() == nil && result.Ref == item.Ref && result.State == rt.StateStopped {
			obs = reconcile.Stopped
		} else if errors.Is(e, rt.ErrConflict) {
			obs = reconcile.Conflict
		}
	} else {
		e := s.runtime.provider.Remove(ctx, item.Ref)
		if e == nil && ctx.Err() == nil {
			observed := reconcile.Observe(ctx, s.runtime.provider, item.Ref)
			if observed == reconcile.Absent && ctx.Err() == nil {
				obs = reconcile.Absent
			} else if observed == reconcile.Conflict && ctx.Err() == nil {
				obs = reconcile.Conflict
			}
		} else if errors.Is(e, rt.ErrConflict) {
			obs = reconcile.Conflict
		}
	}
	if r.Context().Err() != nil {
		return
	}
	item, _, err = s.runtime.store.ObserveAction(r.Context(), id, kind, action.Version, item.Version, obs)
	if err != nil {
		failure(w, 503, "operation outcome unavailable; refresh state")
		return
	}
	send(w, http.StatusAccepted, s.serviceView(r.Context(), item))
}
