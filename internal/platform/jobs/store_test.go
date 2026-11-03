package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestPersistenceAndRecovery(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(privateTempDir(t), "state.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.Enqueue(ctx, "app-a", "deploy", "request-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Enqueue(ctx, "app-a", "deploy", "request-a")
	if err != nil || a.ID != b.ID {
		t.Fatal("request not deduplicated", err)
	}
	if _, err = s.Enqueue(ctx, "app-b", "deploy", "request-a"); !errors.Is(err, ErrConflict) {
		t.Fatal("key reuse accepted", err)
	}
	if _, err = s.Enqueue(ctx, "app-a", "deploy", "request-b"); !errors.Is(err, ErrConflict) {
		t.Fatal("parallel job accepted", err)
	}
	if _, err = s.ClaimNext(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	j, err := s.Get(ctx, a.ID)
	if err != nil || j.Status != "running" {
		t.Fatal(j, err)
	}
	n, err := s.InterruptRunning(ctx)
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	j, err = s.Get(ctx, a.ID)
	if err != nil || j.Status != "interrupted" || j.Error == "" {
		t.Fatal(j, err)
	}
	if _, err = s.Complete(ctx, a.ID, "succeeded", ""); !errors.Is(err, ErrConflict) {
		t.Fatal("completed interrupted job", err)
	}
	if _, err = s.Enqueue(ctx, "app-a", "deploy", "request-b"); err != nil {
		t.Fatal(err)
	}
}
func TestConcurrentClaimsAndCompletion(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(privateTempDir(t), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	job, err := s.Enqueue(ctx, "app", "deploy", "key")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.ClaimNext(ctx); results <- err }()
	}
	wg.Wait()
	close(results)
	success, empty := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrNotFound) {
			empty++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || empty != 1 {
		t.Fatalf("claims: %d %d", success, empty)
	}
	done, err := s.Complete(ctx, job.ID, "succeeded", "")
	if err != nil || done.Status != "succeeded" {
		t.Fatal(done, err)
	}
	if _, err = s.Complete(ctx, job.ID, "failed", "late"); !errors.Is(err, ErrConflict) {
		t.Fatal("second completion accepted", err)
	}
}

func TestListAndFIFO(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(privateTempDir(t), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first, err := s.Enqueue(ctx, "one", "simulation", "key-one")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Enqueue(ctx, "two", "simulation", "key-two")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.List(ctx, 1)
	if err != nil || len(rows) != 1 || rows[0].ID != second.ID {
		t.Fatal(rows, err)
	}
	claimed, err := s.ClaimNext(ctx)
	if err != nil || claimed.ID != first.ID {
		t.Fatal(claimed, err)
	}
	if _, err = s.List(ctx, 101); err == nil {
		t.Fatal("unbounded list accepted")
	}
}

func TestApplicationsPersistAndValidate(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(privateTempDir(t), "state.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	image := "ghcr.io/example/app@sha256:" + strings.Repeat("a", 64)
	if _, err = s.CreateApplication(ctx, "app", "nginx:latest", 80); !errors.Is(err, ErrInvalid) {
		t.Fatal("mutable image accepted", err)
	}
	app, err := s.CreateApplication(ctx, "app", image, 8080)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateApplication(ctx, "app", image, 8080); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.Application(ctx, app.ID)
	if err != nil || got.ID != app.ID || got.Image != image {
		t.Fatal(got, err)
	}
	list, err := s.Applications(ctx)
	if err != nil || len(list) != 1 {
		t.Fatal(list, err)
	}
}
func TestMigrateVersionOnePreservesJobs(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(privateTempDir(t), "state.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE jobs (id TEXT PRIMARY KEY,resource_id TEXT NOT NULL,kind TEXT NOT NULL,idempotency_key TEXT NOT NULL UNIQUE,status TEXT NOT NULL,error TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,updated_at TEXT NOT NULL); INSERT INTO jobs VALUES ('one','app','simulation','key','queued','','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z'); PRAGMA user_version=1;`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	job, err := s.Get(ctx, "one")
	if err != nil || job.Status != "queued" {
		t.Fatal(job, err)
	}
	apps, err := s.Applications(ctx)
	if err != nil || len(apps) != 0 {
		t.Fatal(apps, err)
	}
}

func privateTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestCheckRequiresApplicationsTable(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(privateTempDir(t), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, "DROP TABLE applications"); err != nil {
		t.Fatal(err)
	}
	if err := s.Check(ctx); err == nil {
		t.Fatal("missing applications table reported ready")
	}
}

const testImage = "ghcr.io/example/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(privateTempDir(t), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestForeignKeysEnforcedOnFreshConnection(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	// Force the pool to open a new connection; the DSN pragma must apply to it.
	s.db.SetMaxOpenConns(2)
	conn, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var fk int
	if err = conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
		t.Fatal("foreign_keys not enabled", fk, err)
	}
	if _, err = conn.ExecContext(ctx, "INSERT INTO applications(id,name,image,port,created_at,project_id) VALUES('x','xx','i',80,'t','missing')"); err == nil {
		t.Fatal("dangling project_id accepted")
	}
}

func TestMigrateVersionTwoToThree(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(privateTempDir(t), "state.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE jobs (id TEXT PRIMARY KEY,resource_id TEXT NOT NULL,kind TEXT NOT NULL,idempotency_key TEXT NOT NULL UNIQUE,status TEXT NOT NULL,error TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,updated_at TEXT NOT NULL);
CREATE TABLE applications (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, image TEXT NOT NULL, port INTEGER NOT NULL CHECK(port BETWEEN 1 AND 65535), created_at TEXT NOT NULL);
INSERT INTO applications VALUES ('old','old-app','` + testImage + `',80,'2026-01-01T00:00:00Z'); PRAGMA user_version=2;`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err = os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	app, err := s.Application(ctx, "old")
	if err != nil || app.Version != 1 || app.ProjectID != nil || app.UpdatedAt.Format("2006-01-02") != "2026-01-01" || app.UpdatedAt != app.CreatedAt {
		t.Fatal(app, err)
	}
	var version, idx int
	if err = s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil || version != 3 {
		t.Fatal(version, err)
	}
	if err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='index' AND name='applications_project'").Scan(&idx); err != nil || idx != 1 {
		t.Fatal("index missing", err)
	}
	if err = s.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if app.ServiceUsed {
		t.Fatal("migrated application marked")
	}
	if err = s.MarkRuntimeUsed(ctx, "old"); err != nil {
		t.Fatal(err)
	}
	if app, err = s.Application(ctx, "old"); err != nil || !app.ServiceUsed || app.Version != 1 || !app.UpdatedAt.Equal(app.CreatedAt) {
		t.Fatal("mark must persist without touching version or date", app, err)
	}
	if err = s.MarkRuntimeUsed(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestRejectsNewerDatabase(t *testing.T) {
	path := filepath.Join(privateTempDir(t), "state.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("PRAGMA user_version=4"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err = os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Open(context.Background(), path); err == nil {
		t.Fatal("v4 accepted")
	}
}

func TestProjectsValidationAndUniqueness(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	for _, name := range []string{"", "   ", strings.Repeat("a", 41), "bad\x00name", "tab\tname"} {
		if _, err := s.CreateProject(ctx, name, "blue"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%q accepted: %v", name, err)
		}
	}
	if _, err := s.CreateProject(ctx, "ok", "magenta"); !errors.Is(err, ErrInvalid) {
		t.Fatal("bad color accepted", err)
	}
	p, err := s.CreateProject(ctx, "  Ñandú ", "teal")
	if err != nil || p.Name != "Ñandú" || p.Version != 1 {
		t.Fatal(p, err)
	}
	if _, err = s.CreateProject(ctx, "ñandú", "teal"); !errors.Is(err, ErrConflict) {
		t.Fatal("case-insensitive duplicate accepted", err)
	}
	other, err := s.CreateProject(ctx, "Otro", "red")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.UpdateProject(ctx, other.ID, 1, "ÑANDÚ", "red"); !errors.Is(err, ErrConflict) {
		t.Fatal("rename to existing name accepted", err)
	}
	up, err := s.UpdateProject(ctx, p.ID, 1, "ÑANDÚ", "rose")
	if err != nil || up.Version != 2 || up.Name != "ÑANDÚ" || up.Color != "rose" {
		t.Fatal(up, err)
	}
	if _, err = s.UpdateProject(ctx, p.ID, 1, "x", "rose"); !errors.Is(err, ErrConflict) {
		t.Fatal("stale version accepted", err)
	}
	if _, err = s.UpdateProject(ctx, "nope", 1, "x", "rose"); !errors.Is(err, ErrProjectNotFound) {
		t.Fatal(err)
	}
	if list, err := s.Projects(ctx); err != nil || len(list) != 2 {
		t.Fatal(list, err)
	}
}

func TestProjectLimit(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	for i := 0; i < MaxProjects; i++ {
		if _, err := s.CreateProject(ctx, "p"+strings.Repeat("x", i%30)+string(rune('A'+i/30)), "blue"); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, err := s.CreateProject(ctx, "extra", "blue"); !errors.Is(err, ErrProjectLimit) {
		t.Fatal(err)
	}
}

func TestDeleteProjectDetachesMembers(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	p, _ := s.CreateProject(ctx, "Equipo", "blue")
	a, _ := s.CreateApplication(ctx, "app-a", testImage, 80)
	b, _ := s.CreateApplication(ctx, "app-b", testImage, 80)
	c, _ := s.CreateApplication(ctx, "app-c", testImage, 80)
	if _, err := s.AssignApplications(ctx, &p.ID, []string{a.ID, b.ID}, false); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Application(ctx, a.ID)
	if got.ProjectID == nil || *got.ProjectID != p.ID || got.Version != 1 {
		t.Fatal("assign must set project and keep version", got)
	}
	deleted, members, err := s.DeleteProject(ctx, p.ID)
	if err != nil || deleted.ID != p.ID || len(members) != 2 || members[0] != a.ID || members[1] != b.ID {
		t.Fatal(deleted, members, err)
	}
	for _, id := range []string{a.ID, b.ID, c.ID} {
		app, err := s.Application(ctx, id)
		if err != nil || app.ProjectID != nil {
			t.Fatal("application not detached", app, err)
		}
	}
	if _, _, err = s.DeleteProject(ctx, p.ID); !errors.Is(err, ErrProjectNotFound) {
		t.Fatal(err)
	}
}

func TestAssignApplicationsAtomicAndOnlyUnassigned(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	p1, _ := s.CreateProject(ctx, "Uno", "blue")
	p2, _ := s.CreateProject(ctx, "Dos", "red")
	a, _ := s.CreateApplication(ctx, "app-a", testImage, 80)
	b, _ := s.CreateApplication(ctx, "app-b", testImage, 80)
	missing := "missing"
	if _, err := s.AssignApplications(ctx, &missing, []string{a.ID}, false); !errors.Is(err, ErrProjectNotFound) {
		t.Fatal(err)
	}
	if _, err := s.AssignApplications(ctx, &p1.ID, []string{a.ID, "ghost"}, false); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if got, _ := s.Application(ctx, a.ID); got.ProjectID != nil {
		t.Fatal("partial assignment persisted")
	}
	if _, err := s.AssignApplications(ctx, &p1.ID, []string{a.ID}, false); err != nil {
		t.Fatal(err)
	}
	changed, err := s.AssignApplications(ctx, &p2.ID, []string{a.ID, b.ID, b.ID}, true)
	if err != nil || len(changed) != 1 || changed[0] != b.ID {
		t.Fatal(changed, err)
	}
	if got, _ := s.Application(ctx, a.ID); *got.ProjectID != p1.ID {
		t.Fatal("onlyUnassigned moved an assigned application")
	}
	if _, err = s.AssignApplications(ctx, nil, []string{a.ID, b.ID}, false); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Application(ctx, b.ID); got.ProjectID != nil {
		t.Fatal("not unassigned")
	}
}

func TestUpdateAndDeleteApplication(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	a, _ := s.CreateApplication(ctx, "app-a", testImage, 80)
	if _, err := s.UpdateApplication(ctx, a.ID, 1, "nginx:latest", 80); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := s.UpdateApplication(ctx, a.ID, 1, testImage, 0); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	up, err := s.UpdateApplication(ctx, a.ID, 1, testImage, 9090)
	if err != nil || up.Version != 2 || up.Port != 9090 || up.Name != "app-a" || up.UpdatedAt.Before(a.UpdatedAt) {
		t.Fatal(up, err)
	}
	if _, err = s.UpdateApplication(ctx, a.ID, 1, testImage, 80); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err = s.UpdateApplication(ctx, "ghost", 1, testImage, 80); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err = s.DeleteApplication(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteApplication(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestApplicationJSONKeepsProjectIDNull(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	a, _ := s.CreateApplication(ctx, "app-a", testImage, 80)
	data, _ := json.Marshal(a)
	if !strings.Contains(string(data), `"project_id":null`) || !strings.Contains(string(data), `"image"`) {
		t.Fatal(string(data))
	}
}
