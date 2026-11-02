package configbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/ponytojas/darsena/internal/platform/configvault"
	rt "github.com/ponytojas/darsena/internal/platform/runtime"
)

type receiverFunc func(context.Context, rt.Spec, configvault.Accepted) (rt.State, error)

func (f receiverFunc) Deliver(c context.Context, s rt.Spec, a configvault.Accepted) (rt.State, error) {
	return f(c, s, a)
}

func TestDeliveryRevisionAndRedaction(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "vault.db")
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	vault, err := configvault.Open(ctx, path, id)
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()
	const secret = "sensitive-marker-bridge-746"
	const public = "public-value-bridge-746"
	if _, err = vault.Set(ctx, "app", 0, "TOKEN", secret, true); err != nil {
		t.Fatal(err)
	}
	if _, err = vault.Set(ctx, "app", 1, "REGION", public, false); err != nil {
		t.Fatal(err)
	}
	spec := rt.Spec{Identity: rt.Identity{Installation: "instance", Service: "app", Operation: "operation"}, Image: "example.com/test@sha256:" + strings.Repeat("a", 64), Revision: 2}
	calls := 0
	receiver := receiverFunc(func(ctx context.Context, got rt.Spec, accepted configvault.Accepted) (rt.State, error) {
		calls++
		if got != spec || accepted.Revision() != 2 {
			t.Fatal("incorrect revision or spec")
		}
		// An edit after acceptance must not alter the values handed to this operation.
		if _, err := vault.Set(ctx, "app", 2, "TOKEN", "new-value", true); err != nil {
			t.Fatal(err)
		}
		values := map[string]string{}
		secretFlags := map[string]bool{}
		accepted.Visit(func(name, value string, isSecret bool) { values[name] = value; secretFlags[name] = isSecret })
		if values["TOKEN"] != secret || values["REGION"] != public || !secretFlags["TOKEN"] || secretFlags["REGION"] {
			t.Fatal("incorrect accepted values")
		}
		encoded, _ := json.Marshal(accepted)
		var logs bytes.Buffer
		slog.New(slog.NewJSONHandler(&logs, nil)).Info("accepted", "config", accepted)
		for _, output := range []string{string(encoded), fmt.Sprintf("%+v %#v", accepted, accepted), logs.String()} {
			if strings.Contains(output, secret) || strings.Contains(output, public) {
				t.Fatal("snapshot leaked values")
			}
		}
		return rt.StateRunning, nil
	})
	stale := spec
	stale.Revision = 1
	if _, err := Deliver(ctx, vault, receiver, stale); !errors.Is(err, ErrConflict) || calls != 0 {
		t.Fatalf("stale revision delivered: %v, %d", err, calls)
	}
	state, err := Deliver(ctx, vault, receiver, spec)
	if err != nil || state != rt.StateRunning || calls != 1 {
		t.Fatalf("delivery: %s %v %d", state, err, calls)
	}
	if _, err = Deliver(ctx, vault, receiver, spec); !errors.Is(err, ErrConflict) || calls != 1 {
		t.Fatalf("old snapshot replayed: %v", err)
	}
	// The only persistent copy is encrypted, including SQLite sidecars.
	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		data, err := os.ReadFile(name)
		if err == nil && (bytes.Contains(data, []byte(secret)) || bytes.Contains(data, []byte(public))) {
			t.Fatal("plaintext persisted")
		}
	}
}

func TestUntrustedReceiverErrorsAreRedacted(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	id, _ := age.GenerateX25519Identity()
	v, err := configvault.Open(ctx, filepath.Join(dir, "vault.db"), id)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	const marker = "sensitive-provider-error-178"
	if _, err = v.Set(ctx, "app", 0, "TOKEN", marker, true); err != nil {
		t.Fatal(err)
	}
	spec := rt.Spec{Identity: rt.Identity{Installation: "instance", Service: "app", Operation: "operation"}, Image: "example.com/test@sha256:" + strings.Repeat("a", 64), Revision: 1}
	receiver := receiverFunc(func(context.Context, rt.Spec, configvault.Accepted) (rt.State, error) {
		return rt.StateRunning, errors.New(marker)
	})
	state, err := Deliver(ctx, v, receiver, spec)
	if !errors.Is(err, ErrUnknown) || state != "" || strings.Contains(fmt.Sprint(err), marker) {
		t.Fatalf("unsafe result: %s %v", state, err)
	}
}

type fixedAcceptor struct{ accepted configvault.Accepted }

func (f fixedAcceptor) Accept(context.Context, string, int64) (configvault.Accepted, error) {
	return f.accepted, nil
}

func TestRejectSnapshotFromDifferentResource(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	vault, err := configvault.Open(ctx, filepath.Join(dir, "vault.db"), id)
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()
	if _, err = vault.Set(ctx, "other", 0, "TOKEN", "other-secret", true); err != nil {
		t.Fatal(err)
	}
	accepted, err := vault.Accept(ctx, "other", 1)
	if err != nil {
		t.Fatal(err)
	}
	spec := rt.Spec{Identity: rt.Identity{Installation: "instance", Service: "app", Operation: "operation"}, Image: "example.com/test@sha256:" + strings.Repeat("a", 64), Revision: 1}
	calls := 0
	receiver := receiverFunc(func(context.Context, rt.Spec, configvault.Accepted) (rt.State, error) {
		calls++
		return rt.StateRunning, nil
	})
	state, err := Deliver(ctx, fixedAcceptor{accepted}, receiver, spec)
	if !errors.Is(err, ErrUnavailable) || state != "" || calls != 0 {
		t.Fatalf("foreign snapshot accepted: %s %v calls=%d", state, err, calls)
	}
}
