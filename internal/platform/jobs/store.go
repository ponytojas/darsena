// Package jobs provides durable, local operation state. It does not execute jobs.
package jobs

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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
	// Pragmas in the DSN apply to every connection the pool opens, not only the first.
	dsn := url.URL{Scheme: "file", Path: path}
	query := url.Values{}
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "foreign_keys(1)")
	dsn.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", dsn.String())
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
	if version > 3 {
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
	if version < 3 {
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			return fail(e)
		}
		_, e = tx.ExecContext(ctx, `CREATE TABLE projects (
    id TEXT PRIMARY KEY, name TEXT NOT NULL, name_key TEXT NOT NULL UNIQUE, color TEXT NOT NULL,
    version INTEGER NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
  );
  ALTER TABLE applications ADD COLUMN project_id TEXT REFERENCES projects(id) ON DELETE SET NULL;
  ALTER TABLE applications ADD COLUMN version INTEGER NOT NULL DEFAULT 1;
  ALTER TABLE applications ADD COLUMN updated_at TEXT;
  ALTER TABLE applications ADD COLUMN runtime_used INTEGER NOT NULL DEFAULT 0;
  UPDATE applications SET updated_at = created_at;
  CREATE INDEX applications_project ON applications(project_id);
  PRAGMA user_version = 3;`)
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
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM applications WHERE 0").Scan(&n); err != nil {
		return err
	}
	return s.db.QueryRowContext(ctx, "SELECT count(*) FROM projects WHERE 0").Scan(&n)
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
// ProjectID is nil when the application is not assigned to a project.
type Application struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Image     string    `json:"image"`
	Port      int       `json:"port"`
	CreatedAt time.Time `json:"created_at"`
	ProjectID *string   `json:"project_id"`
	Version   int       `json:"version"`
	UpdatedAt time.Time `json:"updated_at"`
	// ServiceUsed is a permanent mark: a runtime service was once requested for
	// this application, so it cannot be deleted nor change image. It survives
	// restarts in any mode, unlike the runtime's own intention records.
	ServiceUsed bool `json:"service_used"`
}

// Project groups applications; an application belongs to at most one project.
type Project struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Color     string    `json:"color"`
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

var applicationName = regexp.MustCompile(`^[a-z][a-z0-9-]{1,39}$`)
var imageDigest = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/-]*@sha256:[a-f0-9]{64}$`)
var ErrInvalid = errors.New("invalid application definition")
var ErrProjectNotFound = errors.New("project not found")
var ErrProjectLimit = errors.New("project limit reached")

// MaxProjects bounds the number of projects.
const MaxProjects = 100

// maxAssign bounds one bulk assignment.
const maxAssign = 500

// ProjectColors mirrors the accents in web/src/lib/theme.ts.
var ProjectColors = []string{"blue", "cyan", "teal", "green", "lime", "orange", "rust", "red", "rose", "violet", "indigo", "slate"}

func validApplication(image string, port int) bool {
	return len(image) <= 512 && imageDigest.MatchString(image) && port >= 1 && port <= 65535
}

// projectKey validates a project name and returns its trimmed form and its
// uniqueness key. The key is the lower-cased name (full Unicode case mapping, so
// "Ñandú" and "ñandú" collide). golang.org/x/text is not a dependency, so no NFC
// normalisation is applied: canonically equivalent but differently encoded names
// (precomposed vs combining accents) are treated as distinct.
func projectKey(name string) (string, string, bool) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > 40 || !utf8.ValidString(name) {
		return "", "", false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", "", false
		}
	}
	return name, strings.ToLower(name), true
}
func validColor(color string) bool {
	for _, c := range ProjectColors {
		if c == color {
			return true
		}
	}
	return false
}
func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func (s *Store) CreateApplication(ctx context.Context, name, image string, port int) (Application, error) {
	if !applicationName.MatchString(name) || !validApplication(image, port) {
		return Application{}, ErrInvalid
	}
	id, err := randomID()
	if err != nil {
		return Application{}, err
	}
	created := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, "INSERT INTO applications(id,name,image,port,created_at,version,updated_at) VALUES(?,?,?,?,?,1,?) ON CONFLICT(name) DO NOTHING", id, name, image, port, stamp(created), stamp(created))
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
	return Application{ID: id, Name: name, Image: image, Port: port, CreatedAt: created, Version: 1, UpdatedAt: created}, nil
}

const applicationColumns = "id,name,image,port,created_at,project_id,version,updated_at,runtime_used"

func scanApplication(row scanner) (Application, error) {
	var app Application
	var created string
	var project, updated sql.NullString
	var used int
	err := row.Scan(&app.ID, &app.Name, &app.Image, &app.Port, &created, &project, &app.Version, &updated, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return app, ErrNotFound
	}
	if err != nil {
		return app, err
	}
	if app.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return app, err
	}
	app.UpdatedAt = app.CreatedAt
	if updated.Valid {
		if app.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated.String); err != nil {
			return app, err
		}
	}
	app.ServiceUsed = used != 0
	if project.Valid {
		app.ProjectID = &project.String
	}
	return app, nil
}
func (s *Store) Applications(ctx context.Context) ([]Application, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+applicationColumns+" FROM applications ORDER BY name LIMIT 500")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Application{}
	for rows.Next() {
		app, err := scanApplication(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, app)
	}
	return out, rows.Err()
}
func (s *Store) Application(ctx context.Context, id string) (Application, error) {
	return scanApplication(s.db.QueryRowContext(ctx, "SELECT "+applicationColumns+" FROM applications WHERE id=?", id))
}

// UpdateApplication changes image and port if expectedVersion is current. The name is immutable.
func (s *Store) UpdateApplication(ctx context.Context, id string, expectedVersion int, image string, port int) (Application, error) {
	if !validApplication(image, port) {
		return Application{}, ErrInvalid
	}
	res, err := s.db.ExecContext(ctx, "UPDATE applications SET image=?,port=?,version=version+1,updated_at=? WHERE id=? AND version=?", image, port, stamp(time.Now()), id, expectedVersion)
	if err != nil {
		return Application{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Application{}, err
	}
	if n == 0 {
		if _, err = s.Application(ctx, id); err != nil {
			return Application{}, err
		}
		return Application{}, ErrConflict
	}
	return s.Application(ctx, id)
}

// MarkRuntimeUsed permanently records that a runtime service was requested.
// It must complete before the request is dispatched.
func (s *Store) MarkRuntimeUsed(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, "UPDATE applications SET runtime_used=1 WHERE id=?", id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteApplication removes only the definition. Callers must check runtime
// state first; this store knows nothing about it.
func (s *Store) DeleteApplication(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM applications WHERE id=?", id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

const projectColumns = "id,name,color,version,created_at,updated_at"

func scanProject(row scanner) (Project, error) {
	var p Project
	var created, updated string
	err := row.Scan(&p.ID, &p.Name, &p.Color, &p.Version, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrProjectNotFound
	}
	if err != nil {
		return p, err
	}
	if p.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return p, err
	}
	p.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	return p, err
}

// CreateProject returns ErrInvalid, ErrConflict (name already used, ignoring case) or ErrProjectLimit.
func (s *Store) CreateProject(ctx context.Context, name, color string) (Project, error) {
	name, key, ok := projectKey(name)
	if !ok || !validColor(color) {
		return Project{}, ErrInvalid
	}
	id, err := randomID()
	if err != nil {
		return Project{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Project{}, err
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM projects").Scan(&count); err != nil {
		return Project{}, err
	}
	if count >= MaxProjects {
		return Project{}, ErrProjectLimit
	}
	now := stamp(time.Now())
	res, err := tx.ExecContext(ctx, "INSERT INTO projects(id,name,name_key,color,version,created_at,updated_at) VALUES(?,?,?,?,1,?,?) ON CONFLICT(name_key) DO NOTHING", id, name, key, color, now, now)
	if err != nil {
		return Project{}, err
	}
	if n, e := res.RowsAffected(); e != nil {
		return Project{}, e
	} else if n == 0 {
		return Project{}, ErrConflict
	}
	p, err := scanProject(tx.QueryRowContext(ctx, "SELECT "+projectColumns+" FROM projects WHERE id=?", id))
	if err != nil {
		return Project{}, err
	}
	return p, tx.Commit()
}
func (s *Store) Projects(ctx context.Context) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+projectColumns+" FROM projects ORDER BY name_key, id LIMIT ?", MaxProjects)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Project{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Project returns ErrProjectNotFound when absent.
func (s *Store) Project(ctx context.Context, id string) (Project, error) {
	return scanProject(s.db.QueryRowContext(ctx, "SELECT "+projectColumns+" FROM projects WHERE id=?", id))
}

// UpdateProject returns ErrConflict if expectedVersion is stale or the new name is taken.
func (s *Store) UpdateProject(ctx context.Context, id string, expectedVersion int, name, color string) (Project, error) {
	name, key, ok := projectKey(name)
	if !ok || !validColor(color) {
		return Project{}, ErrInvalid
	}
	res, err := s.db.ExecContext(ctx, "UPDATE projects SET name=?,name_key=?,color=?,version=version+1,updated_at=? WHERE id=? AND version=? AND NOT EXISTS (SELECT 1 FROM projects WHERE name_key=? AND id<>?)", name, key, color, stamp(time.Now()), id, expectedVersion, key, id)
	if err != nil {
		return Project{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Project{}, err
	}
	if n == 0 {
		if _, err = s.Project(ctx, id); err != nil {
			return Project{}, err
		}
		return Project{}, ErrConflict
	}
	return s.Project(ctx, id)
}

// DeleteProject detaches its applications (they become unassigned, never deleted)
// and returns the project with the IDs of its former members, for undo.
func (s *Store) DeleteProject(ctx context.Context, id string) (Project, []string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Project{}, nil, err
	}
	defer tx.Rollback()
	p, err := scanProject(tx.QueryRowContext(ctx, "SELECT "+projectColumns+" FROM projects WHERE id=?", id))
	if err != nil {
		return Project{}, nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT id FROM applications WHERE project_id=? ORDER BY name", id)
	if err != nil {
		return Project{}, nil, err
	}
	members := []string{}
	for rows.Next() {
		var member string
		if err = rows.Scan(&member); err != nil {
			rows.Close()
			return Project{}, nil, err
		}
		members = append(members, member)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return Project{}, nil, err
	}
	if err = rows.Close(); err != nil {
		return Project{}, nil, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE applications SET project_id=NULL,updated_at=? WHERE project_id=?", stamp(time.Now()), id); err != nil {
		return Project{}, nil, err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM projects WHERE id=?", id); err != nil {
		return Project{}, nil, err
	}
	return p, members, tx.Commit()
}

// AssignApplications moves applications to a project (nil unassigns), all or nothing.
// ErrProjectNotFound or ErrNotFound abort the whole change. With onlyUnassigned,
// applications that already have a project are left alone (used by undo). It
// returns the IDs actually reassigned. It refreshes updated_at but not version.
func (s *Store) AssignApplications(ctx context.Context, projectID *string, appIDs []string, onlyUnassigned bool) ([]string, error) {
	if len(appIDs) > maxAssign {
		return nil, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if projectID != nil {
		var one int
		err = tx.QueryRowContext(ctx, "SELECT 1 FROM projects WHERE id=?", *projectID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrProjectNotFound
		}
		if err != nil {
			return nil, err
		}
	}
	var target any
	if projectID != nil {
		target = *projectID
	}
	now := stamp(time.Now())
	seen := map[string]bool{}
	changed := []string{}
	for _, id := range appIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		var current sql.NullString
		err = tx.QueryRowContext(ctx, "SELECT project_id FROM applications WHERE id=?", id).Scan(&current)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		if onlyUnassigned && current.Valid {
			continue
		}
		if _, err = tx.ExecContext(ctx, "UPDATE applications SET project_id=?,updated_at=? WHERE id=?", target, now, id); err != nil {
			return nil, err
		}
		changed = append(changed, id)
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return changed, nil
}
