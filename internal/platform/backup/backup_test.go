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

func TestBackupCapturesStateSchemaV3(t *testing.T) {
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
	state := filepath.Join(dir, "state")
	store, err := jobs.Open(ctx, state)
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.CreateProject(ctx, "Equipo", "blue")
	if err != nil {
		t.Fatal(err)
	}
	app, err := store.CreateApplication(ctx, "app-a", "ghcr.io/example/app@sha256:"+strings.Repeat("a", 64), 80)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.AssignApplications(ctx, &p.ID, []string{app.ID}, false); err != nil {
		t.Fatal(err)
	}
	store.Close()
	snap, dest := filepath.Join(dir, "snap"), filepath.Join(dir, "dest")
	if err = Create(ctx, Sources{State: state}, snap); err != nil {
		t.Fatal(err)
	}
	if err = Restore(ctx, snap, dest); err != nil {
		t.Fatal(err)
	}
	restored, err := jobs.Open(ctx, filepath.Join(dest, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	got, err := restored.Application(ctx, app.ID)
	if err != nil || got.ProjectID == nil || *got.ProjectID != p.ID {
		t.Fatal(got, err)
	}
}

// TestRestoreOlderStateV2AndVaultV0 restores a backup made before the v3 state
// schema and the v1 vault schema, then opens both with the current code.
func TestRestoreOlderStateV2AndVaultV0(t *testing.T) {
	ctx := context.Background()
	dir, err := os.MkdirTemp("/tmp", "db-old-")
	if err != nil {
		t.Fatal(err)
	}
	dir, _ = filepath.EvalSymlinks(dir)
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	exec := func(path string, stmts ...any) {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		for i := 0; i < len(stmts); i += 2 {
			args, _ := stmts[i+1].([]any)
			if _, err := db.Exec(stmts[i].(string), args...); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Chmod(path, 0600); err != nil {
			t.Fatal(err)
		}
	}
	state, vault := filepath.Join(dir, "state.db"), filepath.Join(dir, "vault.db")
	exec(state,
		`CREATE TABLE jobs (id TEXT PRIMARY KEY, resource_id TEXT NOT NULL, kind TEXT NOT NULL, idempotency_key TEXT NOT NULL UNIQUE, status TEXT NOT NULL CHECK(status IN ('queued','running','succeeded','failed','interrupted')), error TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`, nil,
		`CREATE TABLE applications (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, image TEXT NOT NULL, port INTEGER NOT NULL CHECK(port BETWEEN 1 AND 65535), created_at TEXT NOT NULL)`, nil,
		`INSERT INTO applications VALUES('app1','old-app','img@sha256:`+strings.Repeat("a", 64)+`',80,'2025-01-01T00:00:00Z')`, nil,
		`PRAGMA user_version=2`, nil)
	id, _ := age.GenerateX25519Identity()
	enc := func(b []byte) []byte {
		var out bytes.Buffer
		w, _ := age.Encrypt(&out, id.Recipient())
		w.Write(b)
		w.Close()
		return out.Bytes()
	}
	doc, _ := json.Marshal(map[string]any{"resource": "app1", "revision": 1, "entries": map[string]any{"K": map[string]any{"value": "old-secret-xyz", "secret": true}}})
	exec(vault,
		`CREATE TABLE metadata (name TEXT PRIMARY KEY,value BLOB NOT NULL)`, nil,
		`CREATE TABLE configurations (resource TEXT PRIMARY KEY,revision INTEGER NOT NULL,ciphertext BLOB NOT NULL)`, nil,
		`CREATE TABLE configuration_history (resource TEXT NOT NULL,revision INTEGER NOT NULL,ciphertext BLOB NOT NULL,PRIMARY KEY(resource,revision))`, nil,
		`INSERT INTO metadata VALUES('identity_check',?)`, []any{enc([]byte("darsena-config-v1"))},
		`INSERT INTO configurations VALUES('app1',1,?)`, []any{enc(doc)},
		`INSERT INTO configuration_history VALUES('app1',1,?)`, []any{enc(doc)})
	snap, dest := filepath.Join(dir, "snap"), filepath.Join(dir, "dest")
	if err = Create(ctx, Sources{State: state, Vault: vault}, snap); err != nil {
		t.Fatal(err)
	}
	if err = Restore(ctx, snap, dest); err != nil {
		t.Fatal(err)
	}
	s, err := jobs.Open(ctx, filepath.Join(dest, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	app, err := s.Application(ctx, "app1")
	if err != nil || app.Name != "old-app" || app.Version != 1 || app.ProjectID != nil {
		t.Fatal(app, err)
	}
	v, err := configvault.Open(ctx, filepath.Join(dest, "vault.db"), id)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	h, err := v.HistoryEntries(ctx, "app1")
	if err != nil || len(h) != 1 || h[0].CreatedAt != nil || len(h[0].Changes) != 1 {
		t.Fatal(h, err)
	}
	if _, err = v.Set(ctx, "app1", 1, "K", "new", true); err != nil {
		t.Fatal(err)
	}
	// A migrated database round-trips through backup again.
	s.Close()
	v.Close()
	snap2 := filepath.Join(dir, "snap2")
	if err = Create(ctx, Sources{State: filepath.Join(dest, "state.db"), Vault: filepath.Join(dest, "vault.db")}, snap2); err != nil {
		t.Fatal(err)
	}
	if err = Restore(ctx, snap2, filepath.Join(dir, "dest2")); err != nil {
		t.Fatal(err)
	}
}
