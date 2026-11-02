// Package execution coordinates one durable runtime create. It is experimental
// and is not connected to the worker or HTTP API.
package execution

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/ponytojas/darsena/internal/platform/configvault"
	"github.com/ponytojas/darsena/internal/platform/runtime/configbridge"

	rt "github.com/ponytojas/darsena/internal/platform/runtime"
	"github.com/ponytojas/darsena/internal/platform/runtime/intent"
	"github.com/ponytojas/darsena/internal/platform/runtime/reconcile"
)

var (
	ErrInvalid  = errors.New("execution: invalid request")
	ErrConflict = errors.New("execution: intent conflict")
	// ErrRevisionConflict is returned only when accepting the requested vault
	// revision fails before this invocation claims or sends a create. Another
	// concurrent invocation may still have claimed the same operation.
	ErrRevisionConflict  = fmt.Errorf("%w: configuration revision", ErrConflict)
	ErrStore             = errors.New("execution: store unavailable")
	ErrAlreadyDispatched = errors.New("execution: operation requires reconciliation")
	ErrUnknown           = errors.New("execution: outcome requires reconciliation")
)

type Store interface {
	Get(context.Context, rt.Identity) (intent.Intent, error)
	Prepare(context.Context, rt.Ref, uint64) (intent.Intent, error)
	MarkDispatched(context.Context, rt.Identity, uint64) (intent.Intent, error)
	RecordObservation(context.Context, rt.Identity, uint64, intent.Phase, reconcile.Observation) (intent.Intent, error)
}
type Creator interface {
	Create(context.Context, rt.Spec) (rt.Result, error)
}
type PrepareRef func(rt.Spec) (rt.Ref, error)

// Result is persisted state, not permission to repeat a mutation. Dispatched
// reports whether this invocation won the dispatch claim, not whether the
// provider received the request.
type Result struct {
	Intent     intent.Intent
	Dispatched bool
}

// Create persists before dispatch and invokes the provider at most once across
// contenders using the same store. After a committed claim, even cancellation,
// crashes or failure to save the result require read-only reconciliation.
// Provider implementations must bind PrepareRef to Create and return only
// observations for the same immutable spec.
func Create(ctx context.Context, store Store, prepare PrepareRef, creator Creator, spec rt.Spec) (Result, error) {
	if creator == nil {
		return Result{}, ErrInvalid
	}
	return create(ctx, store, prepare, spec, nil, func(ctx context.Context, spec rt.Spec, _ configvault.Accepted) (rt.Result, error) {
		return creator.Create(ctx, spec)
	})
}

// ConfiguredCreator receives a revision-bound snapshot only after the durable
// dispatch claim. It must never retain, log, or persist the values.
type ConfiguredCreator interface {
	CreateConfigured(context.Context, rt.Spec, configvault.Accepted) (rt.Result, error)
}

// CreateConfigured accepts the exact current revision before claiming dispatch.
// An edit after acceptance does not modify this snapshot; already dispatched
// intents are never decrypted or sent again.
func CreateConfigured(ctx context.Context, store Store, prepare PrepareRef, creator ConfiguredCreator, vault configbridge.Acceptor, spec rt.Spec) (Result, error) {
	if creator == nil || vault == nil || spec.Revision > math.MaxInt64 {
		return Result{}, ErrInvalid
	}
	return create(ctx, store, prepare, spec, vault, creator.CreateConfigured)
}

func create(ctx context.Context, store Store, prepare PrepareRef, spec rt.Spec, vault configbridge.Acceptor, send func(context.Context, rt.Spec, configvault.Accepted) (rt.Result, error)) (Result, error) {
	var out Result
	if store == nil || prepare == nil || spec.Validate() != nil {
		return out, ErrInvalid
	}
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	ref, err := prepare(spec)
	if err != nil || !ref.Valid() || ref.Identity != spec.Identity || ref.Image != spec.Image || ref.Revision != spec.Revision {
		return out, ErrInvalid
	}
	// First read an existing claim without decrypting. The snapshot for a new
	// attempt is accepted before preparing an intent, so a stale revision cannot
	// reserve an operation that can never be dispatched.
	item, err := store.Get(ctx, ref.Identity)
	if err == nil {
		out.Intent = item
		if item.Ref != ref || item.Revision != spec.Revision {
			return out, ErrConflict
		}
		if item.Phase != intent.Prepared {
			return out, ErrAlreadyDispatched
		}
	} else if !errors.Is(err, intent.ErrNotFound) {
		return out, storeError(err)
	}
	var accepted configvault.Accepted
	if vault != nil {
		accepted, err = vault.Accept(ctx, spec.Identity.Service, int64(spec.Revision))
		if errors.Is(err, configvault.ErrConflict) {
			return out, ErrRevisionConflict
		}
		if err != nil {
			return out, ErrStore
		}
		if accepted.Resource() != spec.Identity.Service || accepted.Revision() != int64(spec.Revision) {
			return out, ErrStore
		}
		if _, err = configbridge.Environment(accepted); err != nil {
			return out, ErrInvalid
		}
	}
	item, err = store.Prepare(ctx, ref, spec.Revision)
	if err != nil {
		return out, storeError(err)
	}
	out.Intent = item
	if item.Phase != intent.Prepared {
		return out, ErrAlreadyDispatched
	}
	item, err = store.MarkDispatched(ctx, ref.Identity, item.Version)
	if err != nil {
		if errors.Is(err, intent.ErrConflict) {
			return out, ErrAlreadyDispatched
		}
		return out, storeError(err)
	}
	out.Intent, out.Dispatched = item, true
	// A cancellation here leaves the durable claim intact, including when no
	// provider request was sent. Conservatively require reconciliation.
	if ctx.Err() != nil {
		return out, ErrUnknown
	}
	result, createErr := send(ctx, spec, accepted)
	observation := reconcile.Unknown
	// A provider may return a stale or foreign result alongside an error. Only
	// a successful create establishes a positive observation; conflict remains
	// explicit and every other failure needs read-only reconciliation.
	if errors.Is(createErr, rt.ErrConflict) {
		observation = reconcile.Conflict
	} else if createErr == nil && result.Ref == ref {
		switch result.State {
		case rt.StateCreated:
			observation = reconcile.Created
		case rt.StateRunning:
			observation = reconcile.Running
		case rt.StateStopped:
			observation = reconcile.Stopped
		}
	}

	// An untrusted provider error is never propagated; even known errors can have
	// occurred after effects. Preserve the claim if saving the observation fails.
	if ctx.Err() != nil {
		return out, ErrUnknown
	}
	saved, err := store.RecordObservation(ctx, ref.Identity, item.Version, intent.Dispatched, observation)
	if err != nil {
		return out, ErrStore
	}
	out.Intent = saved
	if observation == reconcile.Conflict {
		return out, ErrConflict
	}
	if createErr != nil || observation == reconcile.Unknown {
		return out, ErrUnknown
	}
	return out, nil
}

func storeError(err error) error {
	if errors.Is(err, intent.ErrInvalid) {
		return ErrInvalid
	}
	if errors.Is(err, intent.ErrConflict) {
		return ErrConflict
	}
	return ErrStore
}
