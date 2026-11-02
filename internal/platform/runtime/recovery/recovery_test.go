package recovery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	rt "github.com/ponytojas/darsena/internal/platform/runtime"
	"github.com/ponytojas/darsena/internal/platform/runtime/dockerlocal"
	"github.com/ponytojas/darsena/internal/platform/runtime/intent"
	"github.com/ponytojas/darsena/internal/platform/runtime/reconcile"
)

type inspectorFunc func(context.Context, rt.Ref) (rt.Result, error)

func (f inspectorFunc) Inspect(ctx context.Context, ref rt.Ref) (rt.Result, error) {
	return f(ctx, ref)
}

type failingStore struct{ Store }

func (f failingStore) RecordObservation(context.Context, rt.Identity, uint64, intent.Phase, reconcile.Observation) (intent.Intent, error) {
	return intent.Intent{}, errors.New("secret provider details")
}

func newStore(t *testing.T) (*intent.Store, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "intent.db")
	s, err := intent.Open(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	return s, p
}
func prepared(t *testing.T, s *intent.Store, n int, dispatch bool) rt.Ref {
	t.Helper()
	ctx := context.Background()
	ref, err := dockerlocal.PrepareRef(rt.Spec{Identity: rt.Identity{Installation: "install", Service: "service", Operation: fmt.Sprintf("op-%03d", n)}, Image: "registry.example/app@sha256:" + strings.Repeat("b", 64), Revision: 1})
	if err != nil {
		t.Fatal(err)
	}
	item, err := s.Prepare(ctx, ref, 1)
	if err != nil {
		t.Fatal(err)
	}
	if dispatch {
		if _, err = s.MarkDispatched(ctx, ref.Identity, item.Version); err != nil {
			t.Fatal(err)
		}
	}
	return ref
}
func TestRecoveryAfterRestartIsReadOnlyAndBounded(t *testing.T) {
	ctx := context.Background()
	s, path := newStore(t)
	preparedRef := prepared(t, s, 0, false)
	refs := make([]rt.Ref, 205)
	for i := range refs {
		refs[i] = prepared(t, s, i+1, true)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	s, err = intent.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	calls := 0
	inspect := inspectorFunc(func(_ context.Context, ref rt.Ref) (rt.Result, error) {
		calls++
		if ref == refs[0] {
			return rt.Result{}, rt.ErrNotFound
		}
		return rt.Result{Ref: ref, State: rt.StateRunning}, nil
	})
	first, err := Pass(ctx, s, inspect, nil, 120, time.Second)
	if err != nil || first.Processed != 120 || first.Complete {
		t.Fatalf("first: %+v %v", first, err)
	}
	second, err := Pass(ctx, s, inspect, first.Next, 100, time.Second)
	if err != nil || second.Processed != 85 || !second.Complete || calls != 205 {
		t.Fatalf("second: %+v calls=%d %v", second, calls, err)
	}
	p, err := s.Get(ctx, preparedRef.Identity)
	if err != nil || p.Phase != intent.Prepared {
		t.Fatalf("prepared changed: %+v %v", p, err)
	}
	a, err := s.Get(ctx, refs[0].Identity)
	if err != nil || a.Observation != reconcile.Absent {
		t.Fatalf("absent: %+v %v", a, err)
	}
	if _, err = s.MarkDispatched(ctx, refs[0].Identity, a.Version); !errors.Is(err, intent.ErrConflict) {
		t.Fatalf("absence authorized retry: %v", err)
	}
}
func TestConflictDoesNotOverwriteAndCancellationStops(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	defer s.Close()
	ref := prepared(t, s, 1, true)
	inspect := inspectorFunc(func(ctx context.Context, ref rt.Ref) (rt.Result, error) {
		current, err := s.Get(ctx, ref.Identity)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.RecordObservation(ctx, ref.Identity, current.Version, current.Phase, reconcile.Stopped); err != nil {
			t.Fatal(err)
		}
		return rt.Result{Ref: ref, State: rt.StateRunning}, nil
	})
	result, err := Pass(ctx, s, inspect, nil, 10, time.Second)
	if err != nil || result.Conflicts != 1 {
		t.Fatalf("conflict: %+v %v", result, err)
	}
	current, _ := s.Get(ctx, ref.Identity)
	if current.Observation != reconcile.Stopped {
		t.Fatal("concurrent observation overwritten")
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	inspect = func(_ context.Context, ref rt.Ref) (rt.Result, error) {
		cancel()
		return rt.Result{Ref: ref, State: rt.StateRunning}, nil
	}
	_, err = Pass(cancelCtx, s, inspect, nil, 10, time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	after, _ := s.Get(ctx, ref.Identity)
	if after != current {
		t.Fatal("cancellation persisted observation")
	}
}
func TestPersistenceFailureAndTimeout(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	defer s.Close()
	ref := prepared(t, s, 1, true)
	inspect := inspectorFunc(func(context.Context, rt.Ref) (rt.Result, error) { return rt.Result{}, rt.ErrUnavailable })
	_, err := Pass(ctx, failingStore{s}, inspect, nil, 1, time.Second)
	if !errors.Is(err, ErrStore) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("store failure: %v", err)
	}
	current, _ := s.Get(ctx, ref.Identity)
	if current.Phase != intent.Dispatched {
		t.Fatal("failed write changed phase")
	}
	inspect = func(ctx context.Context, _ rt.Ref) (rt.Result, error) { <-ctx.Done(); return rt.Result{}, ctx.Err() }
	_, err = Pass(ctx, s, inspect, nil, 1, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	current, _ = s.Get(ctx, ref.Identity)
	if current.Observation != reconcile.Unknown {
		t.Fatalf("timeout: %+v", current)
	}
}

func TestLateSuccessDoesNotBecomeObservation(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	defer s.Close()
	ref := prepared(t, s, 1, true)
	inspect := inspectorFunc(func(ctx context.Context, ref rt.Ref) (rt.Result, error) {
		<-ctx.Done()
		return rt.Result{Ref: ref, State: rt.StateRunning}, nil
	})
	if _, err := Pass(ctx, s, inspect, nil, 1, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	current, err := s.Get(ctx, ref.Identity)
	if err != nil || current.Observation != reconcile.Unknown {
		t.Fatalf("late success recorded as observation: %+v %v", current, err)
	}
}
