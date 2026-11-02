package execution_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	rt "github.com/ponytojas/darsena/internal/platform/runtime"
	"github.com/ponytojas/darsena/internal/platform/runtime/dockerlocal"
	"github.com/ponytojas/darsena/internal/platform/runtime/execution"
	"github.com/ponytojas/darsena/internal/platform/runtime/intent"
	"github.com/ponytojas/darsena/internal/platform/runtime/reconcile"
)

type creator struct {
	calls atomic.Int32
	fn    func(context.Context, rt.Spec) (rt.Result, error)
}

func (c *creator) Create(ctx context.Context, s rt.Spec) (rt.Result, error) {
	c.calls.Add(1)
	return c.fn(ctx, s)
}
func spec() rt.Spec {
	return rt.Spec{Identity: rt.Identity{Installation: "install", Service: "service", Operation: "op"}, Image: "example.com/app@sha256:" + strings.Repeat("a", 64), Revision: 1}
}
func open(t *testing.T, path string) *intent.Store {
	t.Helper()
	if err := os.Chmod(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	s, e := intent.Open(context.Background(), path)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func TestConcurrentDispatchAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "intent.db")
	a, b := open(t, path), open(t, path)
	c := &creator{fn: func(_ context.Context, s rt.Spec) (rt.Result, error) {
		r, _ := dockerlocal.PrepareRef(s)
		return rt.Result{Ref: r, State: rt.StateRunning}, nil
	}}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, store := range []*intent.Store{a, b} {
		wg.Add(1)
		go func(s *intent.Store) {
			defer wg.Done()
			_, e := execution.Create(ctx, s, dockerlocal.PrepareRef, c, spec())
			errs <- e
		}(store)
	}
	wg.Wait()
	close(errs)
	success, already := 0, 0
	for e := range errs {
		if e == nil {
			success++
		} else if errors.Is(e, execution.ErrAlreadyDispatched) {
			already++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || already != 1 || c.calls.Load() != 1 {
		t.Fatalf("success=%d already=%d calls=%d", success, already, c.calls.Load())
	}
	reopened := open(t, path)
	got, e := reopened.Get(ctx, spec().Identity)
	if e != nil || got.Phase != intent.Observed || got.Observation != reconcile.Running {
		t.Fatalf("got %+v %v", got, e)
	}
	_, e = execution.Create(ctx, reopened, dockerlocal.PrepareRef, c, spec())
	if !errors.Is(e, execution.ErrAlreadyDispatched) || c.calls.Load() != 1 {
		t.Fatalf("retry %v calls %d", e, c.calls.Load())
	}
}
func TestUncertainAndPartialOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state rt.State
		want  reconcile.Observation
	}{{"lost", "", reconcile.Unknown}, {"partial", rt.StateCreated, reconcile.Unknown}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "db")
			s := open(t, path)
			secret := "sensitive-provider-value"
			c := &creator{fn: func(_ context.Context, sp rt.Spec) (rt.Result, error) {
				ref, _ := dockerlocal.PrepareRef(sp)
				return rt.Result{Ref: ref, State: tc.state}, fmt.Errorf("%s", secret)
			}}
			out, e := execution.Create(ctx, s, dockerlocal.PrepareRef, c, spec())
			if !errors.Is(e, execution.ErrUnknown) || strings.Contains(e.Error(), secret) || out.Intent.Observation != tc.want {
				t.Fatalf("out %+v err %v", out, e)
			}
			reopened := open(t, path)
			_, e = execution.Create(ctx, reopened, dockerlocal.PrepareRef, c, spec())
			if !errors.Is(e, execution.ErrAlreadyDispatched) || c.calls.Load() != 1 {
				t.Fatalf("retry %v calls %d", e, c.calls.Load())
			}
		})
	}
}

type failingSave struct{ *intent.Store }

func (s failingSave) RecordObservation(context.Context, rt.Identity, uint64, intent.Phase, reconcile.Observation) (intent.Intent, error) {
	return intent.Intent{}, errors.New("secret database failure")
}
func TestFailureAfterDispatchLeavesRecoverable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "db")
	s := open(t, path)
	c := &creator{fn: func(_ context.Context, sp rt.Spec) (rt.Result, error) {
		r, _ := dockerlocal.PrepareRef(sp)
		return rt.Result{Ref: r, State: rt.StateRunning}, nil
	}}
	_, e := execution.Create(ctx, failingSave{s}, dockerlocal.PrepareRef, c, spec())
	if !errors.Is(e, execution.ErrStore) || strings.Contains(e.Error(), "secret") {
		t.Fatal(e)
	}
	reopened := open(t, path)
	v, e := reopened.Get(ctx, spec().Identity)
	if e != nil || v.Phase != intent.Dispatched {
		t.Fatalf("%+v %v", v, e)
	}
	_, e = execution.Create(ctx, reopened, dockerlocal.PrepareRef, c, spec())
	if !errors.Is(e, execution.ErrAlreadyDispatched) || c.calls.Load() != 1 {
		t.Fatal(e)
	}
}
func TestPriorClaimAndInvalidInputNeverCallProvider(t *testing.T) {
	ctx := context.Background()
	s := open(t, filepath.Join(t.TempDir(), "db"))
	sp := spec()
	ref, _ := dockerlocal.PrepareRef(sp)
	v, e := s.Prepare(ctx, ref, sp.Revision)
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.MarkDispatched(ctx, ref.Identity, v.Version)
	if e != nil {
		t.Fatal(e)
	}
	c := &creator{fn: func(context.Context, rt.Spec) (rt.Result, error) { t.Fatal("unexpected call"); return rt.Result{}, nil }}
	_, e = execution.Create(ctx, s, dockerlocal.PrepareRef, c, sp)
	if !errors.Is(e, execution.ErrAlreadyDispatched) {
		t.Fatal(e)
	}
	sp.Image = "invalid secret"
	_, e = execution.Create(ctx, s, dockerlocal.PrepareRef, c, sp)
	if !errors.Is(e, execution.ErrInvalid) || strings.Contains(e.Error(), "secret") {
		t.Fatal(e)
	}
}

type cancelAfterClaim struct {
	*intent.Store
	cancel context.CancelFunc
}

func (s cancelAfterClaim) MarkDispatched(ctx context.Context, id rt.Identity, version uint64) (intent.Intent, error) {
	v, err := s.Store.MarkDispatched(ctx, id, version)
	s.cancel()
	return v, err
}
func TestCancellationAfterClaimAndUncertainResponse(t *testing.T) {
	t.Run("after claim", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		s := open(t, filepath.Join(t.TempDir(), "db"))
		c := &creator{fn: func(context.Context, rt.Spec) (rt.Result, error) {
			t.Fatal("provider called after cancellation")
			return rt.Result{}, nil
		}}
		out, err := execution.Create(ctx, cancelAfterClaim{s, cancel}, dockerlocal.PrepareRef, c, spec())
		if !errors.Is(err, execution.ErrUnknown) || !out.Dispatched || out.Intent.Phase != intent.Dispatched {
			t.Fatalf("%+v %v", out, err)
		}
		_, err = execution.Create(context.Background(), s, dockerlocal.PrepareRef, c, spec())
		if !errors.Is(err, execution.ErrAlreadyDispatched) {
			t.Fatal(err)
		}
	})
	t.Run("unknown outcome with stale state", func(t *testing.T) {
		s := open(t, filepath.Join(t.TempDir(), "db"))
		c := &creator{fn: func(_ context.Context, sp rt.Spec) (rt.Result, error) {
			ref, _ := dockerlocal.PrepareRef(sp)
			return rt.Result{Ref: ref, State: rt.StateRunning}, rt.ErrUnknownOutcome
		}}
		out, err := execution.Create(context.Background(), s, dockerlocal.PrepareRef, c, spec())
		if !errors.Is(err, execution.ErrUnknown) || out.Intent.Observation != reconcile.Unknown {
			t.Fatalf("%+v %v", out, err)
		}
	})
}

func TestProviderConflictNeverPersistsPositiveState(t *testing.T) {
	s := open(t, filepath.Join(t.TempDir(), "db"))
	c := &creator{fn: func(_ context.Context, sp rt.Spec) (rt.Result, error) {
		ref, _ := dockerlocal.PrepareRef(sp)
		return rt.Result{Ref: ref, State: rt.StateRunning}, fmt.Errorf("sensitive conflict: %w", rt.ErrConflict)
	}}
	out, err := execution.Create(context.Background(), s, dockerlocal.PrepareRef, c, spec())
	if !errors.Is(err, execution.ErrConflict) || strings.Contains(err.Error(), "sensitive") || out.Intent.Observation != reconcile.Conflict {
		t.Fatalf("%+v %v", out, err)
	}
	_, err = execution.Create(context.Background(), s, dockerlocal.PrepareRef, c, spec())
	if !errors.Is(err, execution.ErrAlreadyDispatched) || c.calls.Load() != 1 {
		t.Fatalf("%v %d", err, c.calls.Load())
	}
}
