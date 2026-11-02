package jobs

import (
	"context"
	"database/sql"
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
