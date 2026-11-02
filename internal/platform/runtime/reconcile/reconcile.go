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
	if inspector == nil || !ref.Valid() {
		return Unknown
	}
	result, err := inspector.Inspect(ctx, ref)
	switch {
	case errors.Is(err, rt.ErrNotFound):
		return Absent
	case errors.Is(err, rt.ErrConflict):
		return Conflict
	case errors.Is(err, rt.ErrUnavailable):
		return Unavailable
	case err != nil:
		return Unknown
	case result.Ref != ref:
		return Conflict
	}
	switch result.State {
	case rt.StateCreated:
		return Created
	case rt.StateRunning:
		return Running
	case rt.StateStopped:
		return Stopped
	default:
		return Unknown
	}
}
