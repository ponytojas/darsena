// Package reconcile observes an uncertain runtime operation without mutating it.
// It is not connected to the worker or an API.
package reconcile

import (
	"context"
	"errors"

	rt "github.com/ponytojas/darsena/internal/platform/runtime"
)

type Inspector interface {
	Inspect(context.Context, rt.Ref) (rt.Result, error)
}

type Observation string

const (
	Absent      Observation = "absent"
	Created     Observation = "created"
	Running     Observation = "running"
	Stopped     Observation = "stopped"
	Conflict    Observation = "conflict"
	Unavailable Observation = "unavailable"
	Unknown     Observation = "unknown"
)

// Observe never authorizes a retry, including when a resource is absent.
// An absent observation after a lost response does not prove no effect occurred.
func Observe(ctx context.Context, inspector Inspector, ref rt.Ref) Observation {
	obs, _ := ObserveExit(ctx, inspector, ref)
	return obs
}

// ObserveExit is Observe plus the exit code of a stopped resource, when known.
func ObserveExit(ctx context.Context, inspector Inspector, ref rt.Ref) (Observation, *int) {
	if inspector == nil || !ref.Valid() {
		return Unknown, nil
	}
	result, err := inspector.Inspect(ctx, ref)
	switch {
	case errors.Is(err, rt.ErrNotFound):
		return Absent, nil
	case errors.Is(err, rt.ErrConflict):
		return Conflict, nil
	case errors.Is(err, rt.ErrUnavailable):
		return Unavailable, nil
	case err != nil:
		return Unknown, nil
	case result.Ref != ref:
		return Conflict, nil
	}
	switch result.State {
	case rt.StateCreated:
		return Created, nil
	case rt.StateRunning:
		return Running, nil
	case rt.StateStopped:
		return Stopped, result.ExitCode
	default:
		return Unknown, nil
	}
}
