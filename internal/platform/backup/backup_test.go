package backup

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/ponytojas/darsena/internal/platform/configvault"
	"github.com/ponytojas/darsena/internal/platform/jobs"
	rt "github.com/ponytojas/darsena/internal/platform/runtime"
	"github.com/ponytojas/darsena/internal/platform/runtime/intent"
	"github.com/ponytojas/darsena/internal/platform/worker"
)

func TestBackupRestoreAndWorkerRecovery(t *testing.T) {
	ctx := context.Background()
	dir, err := os.MkdirTemp("/tmp", "db-test-")
	if err != nil {
		t.Fatal(err)
	}
	dir, _ = filepath.EvalSymlinks(dir)
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	state, vault := filepath.Join(dir, "original.db"), filepath.Join(dir, "original-vault.db")
	store, err := jobs.Open(ctx, state)
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.Enqueue(ctx, "app", "deploy", "unique")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ClaimNext(ctx); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	id, _ := age.GenerateX25519Identity()
	v, err := configvault.Open(ctx, vault, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = v.Set(ctx, "app", 0, "PASSWORD", "secret-marker-123", true); err != nil {
		t.Fatal(err)
	}
	v.Close()
	intentPath := filepath.Join(dir, "intent.db")
	is, err := intent.Open(ctx, intentPath)
	if err != nil {
		t.Fatal(err)
	}
	ref := rt.Ref{Identity: rt.Identity{Installation: "installation", Service: "service", Operation: "operation"}, ResourceID: "resource", Fingerprint: strings.Repeat("a", 64), Image: "registry.example/app@sha256:" + strings.Repeat("b", 64), Revision: 1}
	initial, err := is.Prepare(ctx, ref, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = is.MarkDispatched(ctx, ref.Identity, initial.Version); err != nil {
		t.Fatal(err)
	}
	is.Close()
	source := Sources{State: state, Vault: vault, Intent: intentPath}
	snap := filepath.Join(dir, "snapshot")
	if err = Create(ctx, source, snap); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(snap, "vault.db"))
	if err != nil || bytes.Contains(data, []byte("secret-marker-123")) {
		t.Fatal("plaintext in snapshot", err)
	}
	restored := filepath.Join(dir, "restored")
	if err = Restore(ctx, snap, restored); err != nil {
		t.Fatal(err)
	}
	is, err = intent.Open(ctx, filepath.Join(restored, "intent.db"))
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := is.Get(ctx, ref.Identity)
	if err != nil || recovered.Phase != intent.Dispatched {
		t.Fatal(recovered, err)
	}
	is.Close()
	wrong, _ := age.GenerateX25519Identity()
	if bad, e := configvault.Open(ctx, filepath.Join(restored, "vault.db"), wrong); e == nil {
		bad.Close()
		t.Fatal("wrong identity accepted")
	}
	v, err = configvault.Open(ctx, filepath.Join(restored, "vault.db"), id)
	if err != nil {
		t.Fatal(err)
	}
	s, err := v.Get(ctx, "app")
	if err != nil || s.Revision != 1 {
		t.Fatal(s, err)
	}
	v.Close()
	service, err := worker.Open(ctx, filepath.Join(restored, "state.db"), filepath.Join(restored, "socket"))
	if err != nil {
		t.Fatal(err)
	}
	service.Close()
	store, err = jobs.Open(ctx, filepath.Join(restored, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, err := store.Get(ctx, job.ID)
	if err != nil || got.Status != "interrupted" {
		t.Fatal(got, err)
	}
	if err = Restore(ctx, snap, restored); !errors.Is(err, ErrInvalid) {
		t.Fatalf("overwrite: %v", err)
	}
}
func TestRefuseLockedAndCorruptBackup(t *testing.T) {
	ctx := context.Background()
	dir, err := os.MkdirTemp("/tmp", "db-test-")
	if err != nil {
		t.Fatal(err)
	}
	dir, _ = filepath.EvalSymlinks(dir)
	t.Cleanup(func() { os.RemoveAll(dir) })
	os.Chmod(dir, 0700)
	state := filepath.Join(dir, "state")
	s, err := worker.Open(ctx, state, filepath.Join(dir, "socket"))
	if err != nil {
		t.Fatal(err)
	}
	snap := filepath.Join(dir, "snap")
	if err = Create(ctx, Sources{State: state}, snap); !errors.Is(err, ErrBusy) {
		t.Fatalf("lock: %v", err)
	}
	s.Close()
	if err = Create(ctx, Sources{State: state}, snap); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(snap, "state.db"), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteAt([]byte("bad"), 100); err != nil {
		t.Fatal(err)
	}
	f.Close()
	target := filepath.Join(dir, "restore")
	if err = Restore(ctx, snap, target); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("corruption: %v", err)
	}
	if _, err = os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created destination for corrupt snapshot")
	}
}

func TestRejectUnsupportedSchemaAndSnapshotSidecars(t *testing.T) {
	ctx := context.Background()
	dir, err := os.MkdirTemp("/tmp", "backup-")
	if err != nil {
		t.Fatal(err)
	}
	dir, _ = filepath.EvalSymlinks(dir)
	t.Cleanup(func() { os.RemoveAll(dir) })
	state := filepath.Join(dir, "state")
	store, err := jobs.Open(ctx, state)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	snap := filepath.Join(dir, "snap")
	if err = Create(ctx, Sources{State: state}, snap); err != nil {
		t.Fatal(err)
	}
	sidecar := filepath.Join(snap, "state.db-wal")
	if err = os.WriteFile(sidecar, []byte("residual"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = Restore(ctx, snap, filepath.Join(dir, "dest1")); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("sidecar: %v", err)
	}
	os.Remove(sidecar)
	db, err := sql.Open("sqlite", filepath.Join(snap, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("PRAGMA user_version=99"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	e, err := digest(filepath.Join(snap, "state.db"), "state.db")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(manifest{Version: 1, Files: []entry{e}})
	if err = os.WriteFile(filepath.Join(snap, "manifest.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err = Restore(ctx, snap, filepath.Join(dir, "dest2")); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("version: %v", err)
	}
}
