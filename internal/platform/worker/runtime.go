package worker

import (
	"context"
	"errors"
	"math"
	"net/http"
	"time"

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
}

type realRuntime struct {
	installation string
	store        *intent.Store
	prepare      execution.PrepareRef
	provider     RuntimeProvider
	gate         chan struct{}
}

// EnableRuntime opts into experimental operations on the private worker socket.
// The caller must validate and protect the socket and close the service. The
// worker takes ownership of store only after this method succeeds.
func (s *Service) EnableRuntime(installation string, store *intent.Store, prepare execution.PrepareRef, provider RuntimeProvider) error {
	if s == nil || s.vault == nil || s.runtime != nil || store == nil || prepare == nil || provider == nil || !(rt.Identity{Installation: installation, Service: "probe", Operation: "initial"}).Valid() {
		return errors.New("invalid experimental runtime configuration")
	}
	s.runtime = &realRuntime{installation: installation, store: store, prepare: prepare, provider: provider, gate: make(chan struct{}, 1)}
	s.mux.HandleFunc("GET /runtime/info", func(w http.ResponseWriter, r *http.Request) {
		send(w, http.StatusOK, map[string]string{"mode": "experimental", "installation": installation, "policy": "dockerlocal-v3"})
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
}

func (s *Service) serviceView(ctx context.Context, v intent.Intent) serviceResponse {
	view := serviceResponse{ApplicationID: v.Ref.Identity.Service, OperationID: v.Ref.Identity.Operation, Revision: v.Revision, Phase: v.Phase, Observation: v.Observation, Version: v.Version}
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
	id := rt.Identity{Installation: s.runtime.installation, Service: appID, Operation: "initial"}
	if !id.Valid() {
		failure(w, 500, "application unavailable")
		return rt.Identity{}, false
	}
	return id, true
}
func (s *Service) createService(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Revision             uint64 `json:"revision"`
		AcknowledgeNoNetwork bool   `json:"acknowledge_no_network"`
	}
	if !configBody(w, r, &input) {
		return
	}
	if input.Revision == 0 || input.Revision > math.MaxInt64 || !input.AcknowledgeNoNetwork {
		failure(w, 400, "revision and isolated network acknowledgement required")
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
	app, err := s.store.Application(r.Context(), id.Service)
	if err != nil {
		failure(w, 500, "application unavailable")
		return
	}
	spec := rt.Spec{Identity: id, Image: app.Image, Revision: input.Revision}
	if spec.Validate() != nil {
		failure(w, 400, "application is incompatible with isolated runtime")
		return
	}
	// An already-dispatched request is an immutable operation, not a reason to
	// re-read configuration or send a second create after a later edit.
	existing, existingErr := s.runtime.store.Get(r.Context(), id)
	if existingErr == nil {
		if existing.Revision != input.Revision || existing.Ref.Image != app.Image {
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
	obs := reconcile.Observe(ctx, s.runtime.provider, item.Ref)
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
	send(w, 200, s.serviceView(r.Context(), item))
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
