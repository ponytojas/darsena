package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func exampleSpec() Spec {
	return Spec{Identity: Identity{"installation-1", "service-1", "operation-1"}, Image: "registry.example/app@sha256:" + strings.Repeat("a", 64), Revision: 1}
}

func TestValidationDoesNotEchoInput(t *testing.T) {
	valid := exampleSpec()
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	cases := []Spec{valid, valid, valid, valid, valid, valid}
	cases[0].Identity.Installation = "sensitive value"
	cases[1].Identity.Operation = strings.Repeat("x", 64)
	cases[2].Image = "secret:not-a-digest"
	cases[3].Image = "registry.example/app@sha256:" + strings.Repeat("A", 64)
	cases[4].Revision = 0
	cases[5].Image = strings.Repeat("a", 513) + "/app@sha256:" + strings.Repeat("a", 64)
	for _, s := range cases {
		if err := s.Validate(); !errors.Is(err, ErrInvalid) || err.Error() != "runtime: invalid request" {
			t.Fatalf("unexpected validation result: %v", err)
		}
	}
	if !(Ref{Identity: valid.Identity, ResourceID: strings.Repeat("a", 64)}).Valid() {
		t.Fatal("provider's 64-character identifier rejected")
	}
	if (Ref{Identity: valid.Identity, ResourceID: "secret/value"}).Valid() {
		t.Fatal("unsafe provider identifier accepted")
	}
}

// memoryRuntime is a test double, not a provider or production implementation.
// This suite describes the baseline behavior a future adapter must also pass.
type memoryRuntime struct {
	spec      Spec
	result    Result
	present   bool
	failStart bool
}

func (m *memoryRuntime) Create(ctx context.Context, s Spec) (Result, error) {
	if ctx.Err() != nil {
		return Result{}, ErrCancelled
	}
	if err := s.Validate(); err != nil {
		return Result{}, err
	}
	if m.present {
		if m.spec != s {
			return Result{}, ErrConflict
		}
		return m.result, nil
	}
	m.spec, m.present = s, true
	m.result = Result{Ref: Ref{Identity: s.Identity, ResourceID: "resource-1"}, State: StateCreated}
	if m.failStart {
		return m.result, ErrUnavailable
	}
	m.result.State = StateRunning
	return m.result, nil
}
func (m *memoryRuntime) Inspect(ctx context.Context, r Ref) (Result, error) {
	if ctx.Err() != nil {
		return Result{}, ErrCancelled
	}
	if !r.Valid() {
		return Result{}, ErrInvalid
	}
	if !m.present || m.result.Ref != r {
		return Result{}, ErrNotFound
	}
	return m.result, nil
}
func (m *memoryRuntime) Stop(ctx context.Context, r Ref) (Result, error) {
	v, err := m.Inspect(ctx, r)
	if err != nil {
		return Result{}, err
	}
	v.State = StateStopped
	m.result = v
	return v, nil
}
func (m *memoryRuntime) Remove(ctx context.Context, r Ref) error {
	if ctx.Err() != nil {
		return ErrCancelled
	}
	if !r.Valid() {
		return ErrInvalid
	}
	if !m.present {
		return nil
	}
	if m.result.Ref != r {
		return ErrNotFound
	}
	if m.result.State == StateRunning {
		return ErrConflict
	}
	m.present = false
	return nil
}

func TestContractLifecycle(t *testing.T) {
	var provider Runtime = &memoryRuntime{}
	ctx := context.Background()
	s := exampleSpec()
	first, err := provider.Create(ctx, s)
	if err != nil || first.State != StateRunning || !first.Ref.Valid() {
		t.Fatalf("create: %+v %v", first, err)
	}
	repeat, err := provider.Create(ctx, s)
	if err != nil || repeat != first {
		t.Fatalf("repeat: %+v %v", repeat, err)
	}
	changed := s
	changed.Revision++
	if _, err := provider.Create(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed spec: %v", err)
	}
	foreign := first.Ref
	foreign.Identity.Installation = "other-installation"
	if _, err := provider.Inspect(ctx, foreign); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign inspect: %v", err)
	}
	if err := provider.Remove(ctx, foreign); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign remove: %v", err)
	}
	if err := provider.Remove(ctx, first.Ref); !errors.Is(err, ErrConflict) {
		t.Fatalf("remove running: %v", err)
	}
	for range 2 {
		v, err := provider.Stop(ctx, first.Ref)
		if err != nil || v.State != StateStopped {
			t.Fatalf("stop: %+v %v", v, err)
		}
	}
	for range 2 {
		if err := provider.Remove(ctx, first.Ref); err != nil {
			t.Fatalf("remove: %v", err)
		}
	}
	if _, err := provider.Inspect(ctx, first.Ref); !errors.Is(err, ErrNotFound) {
		t.Fatalf("inspect removed: %v", err)
	}
}

func TestContractCancellationAndPartialCreate(t *testing.T) {
	p := &memoryRuntime{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Create(ctx, exampleSpec()); !errors.Is(err, ErrCancelled) || p.present {
		t.Fatalf("cancelled create: %v", err)
	}
	p.failStart = true
	partial, err := p.Create(context.Background(), exampleSpec())
	if !errors.Is(err, ErrUnavailable) || !partial.Ref.Valid() || partial.State != StateCreated {
		t.Fatalf("partial: %+v %v", partial, err)
	}
	inspected, err := p.Inspect(context.Background(), partial.Ref)
	if err != nil || inspected != partial {
		t.Fatalf("inspect partial: %+v %v", inspected, err)
	}
}
