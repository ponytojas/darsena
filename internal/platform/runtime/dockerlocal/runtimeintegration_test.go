//go:build runtimeintegration

package dockerlocal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"

	"filippo.io/age"
	"github.com/ponytojas/darsena/internal/platform/configvault"
	goruntime "runtime"
	"testing"
	"time"

	rt "github.com/ponytojas/darsena/internal/platform/runtime"
)

// These tests never discover a socket, use Docker CLI configuration, or pull an
// image. The lifecycle test is separately enabled because it changes a daemon.
func integrationConfig(t *testing.T) (*Adapter, rt.Spec) {
	t.Helper()
	if goruntime.GOOS != "linux" {
		t.Skip("runtime integration requires a dedicated Linux rootless host")
	}
	if os.Geteuid() == 0 {
		t.Skip("runtime integration requires a non-root account")
	}
	socket, image := os.Getenv("DARSENA_TEST_SOCKET"), os.Getenv("DARSENA_TEST_IMAGE")
	if socket == "" && image == "" {
		t.Skip("set DARSENA_TEST_SOCKET and DARSENA_TEST_IMAGE explicitly")
	}
	if socket == "" || image == "" {
		t.Fatal("both DARSENA_TEST_SOCKET and DARSENA_TEST_IMAGE are required")
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal("cannot generate test identity")
	}
	spec := rt.Spec{Identity: rt.Identity{Installation: "integration", Service: "test", Operation: hex.EncodeToString(random[:])}, Image: image, Revision: 1}
	if err := spec.Validate(); err != nil {
		t.Fatal("test image must be a supported digest-pinned image")
	}
	adapter, err := New(socket)
	if err != nil {
		t.Fatal("explicit socket does not satisfy adapter security requirements")
	}
	return adapter, spec
}

func TestRuntimeIntegrationReadOnly(t *testing.T) {
	adapter, spec := integrationConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := adapter.preflight(ctx, spec.Image); err != nil {
		t.Fatalf("daemon or image does not satisfy adapter preflight: %v", err)
	}
	ref, err := PrepareRef(spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Inspect(ctx, ref); !errors.Is(err, rt.ErrNotFound) {
		t.Fatalf("fresh test reference must be absent; got %v", err)
	}
}

func TestRuntimeIntegrationLifecycle(t *testing.T) {
	if os.Getenv("DARSENA_TEST_RUNTIME_MUTATE") != "1" {
		t.Skip("set DARSENA_TEST_RUNTIME_MUTATE=1 to explicitly allow one container lifecycle")
	}
	adapter, spec := integrationConfig(t)
	ref, err := PrepareRef(spec)
	if err != nil {
		t.Fatal(err)
	}
	// Print the stable reference before dispatch so it remains available even if
	// a response is lost. Never blindly retry or clean up after an uncertain result.
	t.Logf("test resource reference: %s; operation: %s", ref.ResourceID, ref.Identity.Operation)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	created, err := adapter.Create(ctx, spec)
	if err != nil {
		t.Fatalf("create returned %v; preserve and inspect the test reference before any further action", err)
	}
	if created.Ref != ref || created.State != rt.StateRunning {
		t.Fatal("create did not return the expected running resource; preserve it for inspection")
	}
	observed, err := adapter.Inspect(ctx, ref)
	if err != nil || observed.Ref != ref || observed.State != rt.StateRunning {
		t.Fatalf("inspect did not confirm the running resource (%v); preserve it for inspection", err)
	}
	stopped, err := adapter.Stop(ctx, ref)
	if err != nil || stopped.Ref != ref || stopped.State != rt.StateStopped {
		t.Fatalf("stop did not confirm the stopped resource (%v); preserve it for inspection", err)
	}
	if err := adapter.Remove(ctx, ref); err != nil {
		t.Fatalf("remove returned %v; inspect the exact reference before any further action", err)
	}
	if _, err := adapter.Inspect(ctx, ref); !errors.Is(err, rt.ErrNotFound) {
		t.Fatalf("removed resource was not confirmed absent: %v", err)
	}
}

// Uses only synthetic values; inspect verifies them without writing them to logs.
func TestRuntimeIntegrationConfigured(t *testing.T) {
	if os.Getenv("DARSENA_TEST_RUNTIME_MUTATE") != "1" {
		t.Skip("set DARSENA_TEST_RUNTIME_MUTATE=1 to allow one container lifecycle")
	}
	adapter, spec := integrationConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal("identity creation failed")
	}
	v, err := configvault.Open(ctx, filepath.Join(dir, "vault.db"), id)
	if err != nil {
		t.Fatal("vault creation failed")
	}
	defer v.Close()
	if _, err = v.Set(ctx, spec.Identity.Service, 0, "DARSENA_TEST_VALUE", "synthetic-runtime-value", true); err != nil {
		t.Fatal("vault setup failed")
	}
	accepted, err := v.Accept(ctx, spec.Identity.Service, 1)
	if err != nil {
		t.Fatal("accept failed")
	}
	ref, _ := PrepareRef(spec)
	t.Logf("test resource reference: %s; operation: %s", ref.ResourceID, ref.Identity.Operation)
	got, err := adapter.CreateConfigured(ctx, spec, accepted)
	if err != nil || got.State != rt.StateRunning {
		t.Fatalf("configured create failed (%v); preserve reference for inspection", err)
	}
	o, _, err := adapter.inspect(ctx, ref)
	if err != nil || !containsEnv(o.Config.Env, []string{"DARSENA_TEST_VALUE=synthetic-runtime-value"}) {
		t.Fatal("environment not confirmed; preserve reference for inspection")
	}
	if _, err = adapter.Stop(ctx, ref); err != nil {
		t.Fatalf("stop failed (%v); preserve reference", err)
	}
	if err = adapter.Remove(ctx, ref); err != nil {
		t.Fatalf("remove failed (%v); preserve reference", err)
	}
}
