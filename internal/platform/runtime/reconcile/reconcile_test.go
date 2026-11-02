package reconcile

import (
	"context"
	rt "github.com/ponytojas/darsena/internal/platform/runtime"
	"testing"
)

type inspector struct {
	result rt.Result
	err    error
}

func (i inspector) Inspect(context.Context, rt.Ref) (rt.Result, error) { return i.result, i.err }
func TestObserve(t *testing.T) {
	ref := rt.Ref{Identity: rt.Identity{Installation: "install", Service: "service", Operation: "operation"}, ResourceID: "resource"}
	cases := []struct {
		result rt.Result
		err    error
		want   Observation
	}{
		{rt.Result{Ref: ref, State: rt.StateRunning}, nil, Running},
		{rt.Result{Ref: ref, State: rt.StateCreated}, nil, Created},
		{rt.Result{Ref: ref, State: rt.StateStopped}, nil, Stopped},
		{rt.Result{}, rt.ErrNotFound, Absent}, {rt.Result{}, rt.ErrConflict, Conflict},
		{rt.Result{}, rt.ErrUnavailable, Unavailable}, {rt.Result{}, rt.ErrTimeout, Unknown},
		{rt.Result{State: rt.StateRunning}, nil, Conflict},
	}
	for _, c := range cases {
		if got := Observe(context.Background(), inspector{c.result, c.err}, ref); got != c.want {
			t.Fatalf("got %s want %s", got, c.want)
		}
	}
}
