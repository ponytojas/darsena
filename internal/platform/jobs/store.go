// Package jobs provides durable, local operation state. It does not execute jobs.
package jobs

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("job not found")
var ErrConflict = errors.New("job conflicts with current state")

type Job struct {
	ID             string
	ResourceID     string
	Kind           string
	IdempotencyKey string
	Status         string
	Error          string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}
type Store struct{ db *sql.DB }

// Open creates a private local database. One process must own it at a time; callers
// must not run recovery while another worker may be executing jobs.
func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" || path == ":memory:" || strings.HasPrefix(strings.ToLower(path), "file:") {
		return nil, fmt.Errorf("a persistent database path is required")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	dir, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	if !dir.IsDir() || dir.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("state directory must be private")
	}
	infoPath, err := os.Lstat(path)
	if err == nil && (!infoPath.Mode().IsRegular() || infoPath.Mode().Perm()&0077 != 0) {
		return nil, fmt.Errorf("state database must be a private regular file")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	fail := func(err error) (*Store, error) { _ = db.Close(); return nil, err }
	if _, err = db.ExecContext(ctx, "PRAGMA busy_timeout = 5000; PRAGMA journal_mode = WAL; PRAGMA foreign_keys = ON;"); err != nil {
		return fail(err)
	}
	var version int
	if err = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fail(err)
	}
	if version > 2 {
		return fail(fmt.Errorf("unsupported database version %d", version))
	}
	if version == 0 {
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			return fail(e)
		}
		_, e = tx.ExecContext(ctx, `CREATE TABLE jobs (
    id TEXT PRIMARY KEY, resource_id TEXT NOT NULL, kind TEXT NOT NULL, idempotency_key TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL CHECK(status IN ('queued','running','succeeded','failed','interrupted')),
    error TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL
  );
  CREATE UNIQUE INDEX jobs_one_active_per_resource ON jobs(resource_id) WHERE status IN ('queued','running');
  CREATE INDEX jobs_status_created ON jobs(status,created_at,id);
  PRAGMA user_version = 1;`)
		if e != nil {
			_ = tx.Rollback()
			return fail(e)
		}
		if e = tx.Commit(); e != nil {
			return fail(e)
		}
	}
	if version < 2 {
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			return fail(e)
		}
		_, e = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS applications (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, image TEXT NOT NULL, port INTEGER NOT NULL CHECK(port BETWEEN 1 AND 65535), created_at TEXT NOT NULL); PRAGMA user_version = 2;`)
		if e != nil {
			_ = tx.Rollback()
			return fail(e)
		}
		if e = tx.Commit(); e != nil {
			return fail(e)
		}
	}
	return &Store{db: db}, nil
}
func (s *Store) Close() error { return s.db.Close() }

// Check verifies access to both schemas; it does not audit their contents.
func (s *Store) Check(ctx context.Context) error {
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM jobs WHERE 0").Scan(&n); err != nil {
		return err
	}
	return s.db.QueryRowContext(ctx, "SELECT count(*) FROM applications WHERE 0").Scan(&n)
}
func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

const columns = "id,resource_id,kind,idempotency_key,status,error,created_at,updated_at"

type scanner interface{ Scan(...any) error }

func scan(row scanner) (Job, error) {
	var j Job
	var created, updated string
	err := row.Scan(&j.ID, &j.ResourceID, &j.Kind, &j.IdempotencyKey, &j.Status, &j.Error, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return j, ErrNotFound
	}
	if err != nil {
		return j, err
	}
	j.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return j, err
	}
	j.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	return j, err
}
func (s *Store) Get(ctx context.Context, id string) (Job, error) {
	return scan(s.db.QueryRowContext(ctx, "SELECT "+columns+" FROM jobs WHERE id=?", id))
}
func valid(value string, max int) bool { return strings.TrimSpace(value) != "" && len(value) <= max }

// Enqueue deduplicates an identical request; a key reused for a different request fails.
func (s *Store) Enqueue(ctx context.Context, resource, kind, key string) (Job, error) {
	if !valid(resource, 128) || !valid(kind, 128) || !valid(key, 200) {
		return Job{}, fmt.Errorf("resource, kind and idempotency key are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	existing, err := scan(tx.QueryRowContext(ctx, "SELECT "+columns+" FROM jobs WHERE idempotency_key=?", key))
	if err == nil {
		if existing.ResourceID != resource || existing.Kind != kind {
			return Job{}, ErrConflict
		}
		return existing, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Job{}, err
	}
	var active int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM jobs WHERE resource_id=? AND status IN ('queued','running')", resource).Scan(&active); err != nil {
		return Job{}, err
	}
	if active != 0 {
		return Job{}, ErrConflict
	}
	id, err := randomID()
	if err != nil {
		return Job{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, "INSERT INTO jobs (id,resource_id,kind,idempotency_key,status,created_at,updated_at) VALUES (?,?,?,?, 'queued',?,?)", id, resource, kind, key, now, now)
	if err != nil {
		return Job{}, fmt.Errorf("enqueue: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return Job{}, err
	}
	return s.Get(ctx, id)
}

// ClaimNext atomically claims the oldest queued job, or returns ErrNotFound.
func (s *Store) ClaimNext(ctx context.Context) (Job, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	j, err := scan(tx.QueryRowContext(ctx, "SELECT "+columns+" FROM jobs WHERE status='queued' ORDER BY rowid LIMIT 1"))
	if err != nil {
		return Job{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := tx.ExecContext(ctx, "UPDATE jobs SET status='running',updated_at=? WHERE id=? AND status='queued'", now, j.ID)
	if err != nil {
		return Job{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Job{}, err
	}
	if n != 1 {
		return Job{}, ErrConflict
	}
	if err = tx.Commit(); err != nil {
		return Job{}, err
	}
	return s.Get(ctx, j.ID)
}

// Complete changes only a running job to a terminal state.
func (s *Store) Complete(ctx context.Context, id, status, message string) (Job, error) {
	if status != "succeeded" && status != "failed" {
		return Job{}, fmt.Errorf("invalid terminal status")
	}
	if status == "succeeded" && message != "" {
		return Job{}, fmt.Errorf("successful job cannot have an error")
	}
	if len(message) > 4096 {
		return Job{}, fmt.Errorf("error message too long")
	}
	res, err := s.db.ExecContext(ctx, "UPDATE jobs SET status=?,error=?,updated_at=? WHERE id=? AND status='running'", status, message, time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return Job{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Job{}, err
	}
	if n == 0 {
		if _, err = s.Get(ctx, id); err != nil {
			return Job{}, err
		}
		return Job{}, ErrConflict
	}
	return s.Get(ctx, id)
}

// InterruptRunning is explicit: only call after exclusive ownership is established.
// Queued jobs remain queued. External effects must be inspected before retrying.
func (s *Store) InterruptRunning(ctx context.Context) (int64, error) {
	var count int64
	for {
		ids, err := s.InterruptRunningBatch(ctx)
		if err != nil {
			return count, err
		}
		count += int64(len(ids))
		if len(ids) == 0 {
			return count, nil
		}
	}
}

// InterruptRunningBatch commits at most 100 jobs and returns their IDs only after commit.
// Call only while holding exclusive worker ownership.
func (s *Store) InterruptRunningBatch(ctx context.Context) ([]string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT rowid,id FROM jobs WHERE status='running' ORDER BY rowid LIMIT 100")
	if err != nil {
		return nil, err
	}
	type entry struct {
		rowid int64
		id    string
	}
	var entries []entry
	for rows.Next() {
		var e entry
		if err = rows.Scan(&e.rowid, &e.id); err != nil {
			rows.Close()
			return nil, err
		}
		entries = append(entries, e)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		_, err = tx.ExecContext(ctx, "UPDATE jobs SET status='interrupted',error=?,updated_at=? WHERE rowid=? AND status='running'", "Worker stopped before the result was recorded; inspect external effects before retrying.", time.Now().UTC().Format(time.RFC3339Nano), e.rowid)
		if err != nil {
			return nil, err
		}
		ids = append(ids, e.id)
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return ids, nil
}

// List returns the newest jobs first, with a bounded result size.
func (s *Store) List(ctx context.Context, limit int) ([]Job, error) {
	if limit < 1 || limit > 100 {
		return nil, fmt.Errorf("limit must be between 1 and 100")
	}
	rows, err := s.db.QueryContext(ctx, "SELECT "+columns+" FROM jobs ORDER BY rowid DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Job, 0)
	for rows.Next() {
		j, err := scan(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, j)
	}
	return result, rows.Err()
}

// Application is a persisted definition. No infrastructure is changed when creating it.
type Application struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Image     string    `json:"image"`
	Port      int       `json:"port"`
	CreatedAt time.Time `json:"created_at"`
}

var applicationName = regexp.MustCompile(`^[a-z][a-z0-9-]{1,39}$`)
var imageDigest = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/-]*@sha256:[a-f0-9]{64}$`)
var ErrInvalid = errors.New("invalid application definition")

func (s *Store) CreateApplication(ctx context.Context, name, image string, port int) (Application, error) {
	if !applicationName.MatchString(name) || len(image) > 512 || !imageDigest.MatchString(image) || port < 1 || port > 65535 {
		return Application{}, ErrInvalid
	}
	id, err := randomID()
	if err != nil {
		return Application{}, err
	}
	created := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, "INSERT INTO applications(id,name,image,port,created_at) VALUES(?,?,?,?,?) ON CONFLICT(name) DO NOTHING", id, name, image, port, created.Format(time.RFC3339Nano))
	if err != nil {
		return Application{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return Application{}, err
	}
	if count == 0 {
		return Application{}, ErrConflict
	}
	return Application{ID: id, Name: name, Image: image, Port: port, CreatedAt: created}, nil
}
func (s *Store) Applications(ctx context.Context) ([]Application, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,name,image,port,created_at FROM applications ORDER BY name LIMIT 500")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Application{}
	for rows.Next() {
		var app Application
		var date string
		if err = rows.Scan(&app.ID, &app.Name, &app.Image, &app.Port, &date); err != nil {
			return nil, err
		}
		app.CreatedAt, err = time.Parse(time.RFC3339Nano, date)
		if err != nil {
			return nil, err
		}
		out = append(out, app)
	}
	return out, rows.Err()
}
func (s *Store) Application(ctx context.Context, id string) (Application, error) {
	var app Application
	var date string
	err := s.db.QueryRowContext(ctx, "SELECT id,name,image,port,created_at FROM applications WHERE id=?", id).Scan(&app.ID, &app.Name, &app.Image, &app.Port, &date)
	if errors.Is(err, sql.ErrNoRows) {
		return app, ErrNotFound
	}
	if err != nil {
		return app, err
	}
	app.CreatedAt, err = time.Parse(time.RFC3339Nano, date)
	return app, err
}
