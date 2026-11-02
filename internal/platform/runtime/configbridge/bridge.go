// Package configbridge explores revision-bound, in-memory configuration delivery.
// It is not connected to the worker, execution coordinator, or a real provider.
package configbridge

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strings"

	"github.com/ponytojas/darsena/internal/platform/configvault"
	rt "github.com/ponytojas/darsena/internal/platform/runtime"
)

var (
	ErrInvalid     = errors.New("configuration delivery: invalid request")
	ErrConflict    = errors.New("configuration delivery: revision conflict")
	ErrUnavailable = errors.New("configuration delivery: unavailable")
	ErrUnknown     = errors.New("configuration delivery: outcome unknown")
)

// Acceptor returns the current immutable snapshot only when its revision matches.
// A future implementation must preserve Vault.Accept's atomic read semantics.
type Acceptor interface {
	Accept(context.Context, string, int64) (configvault.Accepted, error)
}

// Receiver is an experimental in-memory boundary. Implementations must not log,
// persist, serialize, or retain the snapshot; they must respect ctx. It is not
// the real provider interface and does not confer permission to dispatch.
type Receiver interface {
	Deliver(context.Context, rt.Spec, configvault.Accepted) (rt.State, error)
}

// Deliver accepts exactly the current revision for the service named in spec,
// then hands that immutable snapshot to a receiver. An edit after acceptance is
// not applied automatically. There is no transaction spanning vault and runtime.
// No intent, authorization, or crash-safe dispatch is implemented here: callers
// must not use this bridge for real operations until those boundaries are joined.
func Deliver(ctx context.Context, vault Acceptor, receiver Receiver, spec rt.Spec) (rt.State, error) {
	if vault == nil || receiver == nil || spec.Validate() != nil || spec.Revision > math.MaxInt64 {
		return "", ErrInvalid
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	accepted, err := vault.Accept(ctx, spec.Identity.Service, int64(spec.Revision))
	if err != nil {
		switch {
		case errors.Is(err, configvault.ErrConflict):
			return "", ErrConflict
		case errors.Is(err, configvault.ErrInvalid):
			return "", ErrInvalid
		case ctx.Err() != nil:
			return "", ctx.Err()
		default:
			return "", ErrUnavailable
		}
	}
	// Treat a broken implementation of the boundary as unavailable, not as a
	// reason to deliver a snapshot from another revision.
	if accepted.Resource() != spec.Identity.Service || accepted.Revision() != int64(spec.Revision) {
		return "", ErrUnavailable
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	state, err := receiver.Deliver(ctx, spec, accepted)
	// Errors are untrusted and can contain inputs. After delivery starts even a
	// cancellation can have effects, so report uncertainty and never retry.
	if err != nil || ctx.Err() != nil {
		return "", ErrUnknown
	}
	switch state {
	case rt.StateCreated, rt.StateRunning, rt.StateStopped:
		return state, nil
	default:
		return "", ErrUnknown
	}
}

// Environment converts an accepted snapshot to a bounded Docker environment.
// The returned values are sensitive and must only be held for the duration of
// a request; callers must never log, persist or return them to clients.
func Environment(accepted configvault.Accepted) ([]string, error) {
	var env []string
	valid := true
	accepted.Visit(func(name, value string, _ bool) {
		if !envName.MatchString(name) || strings.IndexByte(value, 0) >= 0 {
			valid = false
			return
		}
		env = append(env, name+"="+value)
	})
	// JSON expansion is included in the limit so the enclosing Engine request
	// stays well below its 1 MiB maximum even for escaped control characters.
	encoded, err := json.Marshal(env)
	if !valid || err != nil || len(encoded) > 256*1024 {
		return nil, ErrInvalid
	}
	return env, nil
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
