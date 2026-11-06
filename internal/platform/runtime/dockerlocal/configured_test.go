package dockerlocal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"filippo.io/age"
	"github.com/ponytojas/darsena/internal/platform/configvault"
	rt "github.com/ponytojas/darsena/internal/platform/runtime"
	"github.com/ponytojas/darsena/internal/platform/runtime/execution"
	"github.com/ponytojas/darsena/internal/platform/runtime/intent"
)

func configuredVault(t *testing.T) *configvault.Vault {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	v, err := configvault.Open(context.Background(), filepath.Join(dir, "vault.db"), id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { v.Close() })
	return v
}
func TestConfiguredExecutionEngineAndReplay(t *testing.T) {
	ctx := context.Background()
	v := configuredVault(t)
	const secret = "private-value-test-493"
	const public = "region-test-493"
	if _, err := v.Set(ctx, "service-1", 0, "TOKEN", secret, true); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Set(ctx, "service-1", 1, "REGION", public, false); err != nil {
		t.Fatal(err)
	}
	sp := spec()
	sp.Revision = 2
	e := fake()
	a := newAdapter(e)
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	path := filepath.Join(dir, "intents.db")
	store, err := intent.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	out, err := execution.CreateConfigured(ctx, store, PrepareRef, a, v, sp)
	if err != nil || !out.Dispatched {
		t.Fatalf("result %v", err)
	}
	config := e.container["Config"].(map[string]any)
	env := config["Env"].([]any)
	if len(env) != 2 || env[0] != "REGION="+public || env[1] != "TOKEN="+secret {
		t.Fatal("incorrect runtime values")
	}
	if _, err := v.Set(ctx, "service-1", 2, "TOKEN", "new", true); err != nil {
		t.Fatal(err)
	}
	_, err = execution.CreateConfigured(ctx, store, PrepareRef, a, v, sp)
	if !errors.Is(err, execution.ErrAlreadyDispatched) || e.posts != 1 {
		t.Fatalf("unexpected retry: %v", err)
	}
	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		b, err := os.ReadFile(name)
		if err == nil && (strings.Contains(string(b), secret) || strings.Contains(string(b), public)) {
			t.Fatal("intent leaked values")
		}
	}
}

type configuredCounter struct{ calls atomic.Int32 }

func (c *configuredCounter) CreateConfigured(_ context.Context, s rt.Spec, a configvault.Accepted) (rt.Result, error) {
	c.calls.Add(1)
	ref, _ := PrepareRef(s)
	return rt.Result{Ref: ref, State: rt.StateRunning}, nil
}
func TestConfiguredRevisionAndConcurrentClaim(t *testing.T) {
	ctx := context.Background()
	v := configuredVault(t)
	if _, err := v.Set(ctx, "service-1", 0, "TOKEN", "sensitive", true); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	path := filepath.Join(dir, "i.db")
	a, err := intent.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := intent.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	sp := spec()
	c := new(configuredCounter)
	stale := sp
	stale.Revision = 2
	if _, err := execution.CreateConfigured(ctx, a, PrepareRef, c, v, stale); !errors.Is(err, execution.ErrRevisionConflict) || !errors.Is(err, execution.ErrConflict) || c.calls.Load() != 0 {
		t.Fatalf("stale %v", err)
	}
	// The stale request must not reserve this identity; concurrent requests for
	// the current revision can still claim it exactly once.
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, s := range []*intent.Store{a, b} {
		wg.Add(1)
		go func(s *intent.Store) {
			defer wg.Done()
			_, err := execution.CreateConfigured(ctx, s, PrepareRef, c, v, sp)
			errs <- err
		}(s)
	}
	wg.Wait()
	close(errs)
	ok, conflict := 0, 0
	for err := range errs {
		if err == nil {
			ok++
		} else if errors.Is(err, execution.ErrAlreadyDispatched) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if ok != 1 || conflict != 1 || c.calls.Load() != 1 {
		t.Fatalf("dispatches %d %d %d", ok, conflict, c.calls.Load())
	}
}
func TestConfiguredEnvironmentMismatchDoesNotStart(t *testing.T) {
	ctx := context.Background()
	v := configuredVault(t)
	if _, err := v.Set(ctx, "service-1", 0, "TOKEN", "secret", true); err != nil {
		t.Fatal(err)
	}
	accepted, err := v.Accept(ctx, "service-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	e := fake()
	e.tamperEnv = true
	a := newAdapter(e)
	_, err = a.CreateConfigured(ctx, spec(), accepted)
	if !errors.Is(err, rt.ErrConflict) || e.startCalls != 0 {
		t.Fatalf("mismatch %v starts %d", err, e.startCalls)
	}
}

func TestConfiguredOversizeRejectedBeforeDispatch(t *testing.T) {
	ctx := context.Background()
	v := configuredVault(t)
	value := strings.Repeat("x", 65536)
	for i, name := range []string{"A", "B", "C", "D", "E"} {
		if _, err := v.Set(ctx, "service-1", int64(i), name, value, true); err != nil {
			t.Fatal(err)
		}
	}
	sp := spec()
	sp.Revision = 5
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	store, err := intent.Open(ctx, filepath.Join(dir, "intent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	c := new(configuredCounter)
	out, err := execution.CreateConfigured(ctx, store, PrepareRef, c, v, sp)
	if !errors.Is(err, execution.ErrInvalid) || out.Dispatched || c.calls.Load() != 0 {
		t.Fatalf("oversize accepted: %v", err)
	}
	_, err = store.Get(ctx, sp.Identity)
	if !errors.Is(err, intent.ErrNotFound) {
		t.Fatal("invalid snapshot must not reserve an intention")
	}
}

type changeAfterAccept struct{ v *configvault.Vault }

func (a changeAfterAccept) Accept(ctx context.Context, resource string, revision int64) (configvault.Accepted, error) {
	snapshot, err := a.v.Accept(ctx, resource, revision)
	if err != nil {
		return snapshot, err
	}
	_, err = a.v.Set(ctx, resource, revision, "TOKEN", "new-value", true)
	return snapshot, err
}

type snapshotCreator struct{ value string }

func (c *snapshotCreator) CreateConfigured(_ context.Context, spec rt.Spec, snapshot configvault.Accepted) (rt.Result, error) {
	snapshot.Visit(func(name, value string, secret bool) {
		if name == "TOKEN" {
			c.value = value
		}
	})
	ref, _ := PrepareRef(spec)
	return rt.Result{Ref: ref, State: rt.StateRunning}, nil
}
func TestAcceptedSnapshotSurvivesConcurrentConfigurationChange(t *testing.T) {
	ctx := context.Background()
	v := configuredVault(t)
	if _, err := v.Set(ctx, "service-1", 0, "TOKEN", "old-value", true); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := intent.Open(ctx, filepath.Join(dir, "intent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	creator := new(snapshotCreator)
	_, err = execution.CreateConfigured(ctx, store, PrepareRef, creator, changeAfterAccept{v}, spec())
	if err != nil || creator.value != "old-value" {
		t.Fatalf("accepted revision was not retained: %v", err)
	}
	current, err := v.Get(ctx, "service-1")
	if err != nil || current.Revision != 2 {
		t.Fatalf("update not persisted: %v", err)
	}
}
