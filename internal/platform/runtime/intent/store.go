// Package intent stores immutable runtime intentions. It never invokes a runtime.
// This experimental store is not connected to the worker or public API.
package intent

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	rt "github.com/ponytojas/darsena/internal/platform/runtime"
	"github.com/ponytojas/darsena/internal/platform/runtime/reconcile"
	_ "modernc.org/sqlite"
)

var (
	ErrInvalid     = errors.New("intent: invalid request")
	ErrConflict    = errors.New("intent: conflict")
	ErrNotFound    = errors.New("intent: not found")
	ErrUnavailable = errors.New("intent: store unavailable")
)

type Phase string

const (
	Prepared   Phase = "prepared"   // No mutation has been dispatched.
	Dispatched Phase = "dispatched" // Persist before dispatch; outcome may be unknown.
	Observed   Phase = "observed"   // An observation is recorded, never permission to retry.
)

type Intent struct {
	Ref         rt.Ref
	Revision    uint64
	Phase       Phase
	Observation reconcile.Observation
	Version     uint64 // Optimistic concurrency token, incremented on each transition.
}

type Store struct{ db *sql.DB }

var fingerprint = regexp.MustCompile(`^[a-f0-9]{64}$`)

// Open requires a private directory and private regular SQLite files. Callers
// must ensure exclusive ownership of the directory; path checks are not atomic.
func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" || path == ":memory:" || strings.HasPrefix(strings.ToLower(path), "file:") {
		return nil, ErrInvalid
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, ErrInvalid
	}
	dir, err := os.Lstat(filepath.Dir(path))
	if err != nil || !dir.IsDir() || dir.Mode()&os.ModeSymlink != 0 || dir.Mode().Perm()&0077 != 0 {
		return nil, ErrInvalid
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		info, e := os.Lstat(p)
		if e == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0) {
			return nil, ErrInvalid
		}
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return nil, ErrUnavailable
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, ErrUnavailable
	}
	if f.Close() != nil {
		return nil, ErrUnavailable
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, ErrUnavailable
	}
	db.SetMaxOpenConns(1)
	fail := func() (*Store, error) { _ = db.Close(); return nil, ErrUnavailable }
	if _, err = db.ExecContext(ctx, `PRAGMA busy_timeout=5000; PRAGMA journal_mode=WAL;`); err != nil {
		return fail()
	}
	var version int
	if err = db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil || version > 2 {
		return fail()
	}
	if version == 0 {
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			return fail()
		}
		_, e = tx.ExecContext(ctx, `CREATE TABLE runtime_intents (
 installation TEXT NOT NULL, service TEXT NOT NULL, operation TEXT NOT NULL,
 resource TEXT NOT NULL, fingerprint TEXT NOT NULL, image TEXT NOT NULL, revision INTEGER NOT NULL CHECK(revision > 0),
 phase TEXT NOT NULL CHECK(phase IN ('prepared','dispatched','observed')),
 observation TEXT NOT NULL CHECK(observation IN ('','absent','created','running','stopped','conflict','unavailable','unknown')),
 version INTEGER NOT NULL CHECK(version > 0), PRIMARY KEY(installation,service,operation)); PRAGMA user_version=1;`)
		if e != nil {
			_ = tx.Rollback()
			return fail()
		}
		if tx.Commit() != nil {
			return fail()
		}
		version = 1
	}
	if version == 1 {
		_, err = db.ExecContext(ctx, `BEGIN IMMEDIATE; CREATE TABLE runtime_actions (
 installation TEXT NOT NULL, service TEXT NOT NULL, operation TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('stop','remove')), phase TEXT NOT NULL CHECK(phase IN ('dispatched','observed')),
 observation TEXT NOT NULL CHECK(observation IN ('','absent','created','running','stopped','conflict','unavailable','unknown')),
 version INTEGER NOT NULL CHECK(version > 0), PRIMARY KEY(installation,service,operation,kind),
 FOREIGN KEY(installation,service,operation) REFERENCES runtime_intents(installation,service,operation)); PRAGMA user_version=2; COMMIT;`)
		if err != nil {
			_, _ = db.ExecContext(context.Background(), `ROLLBACK`)
			return fail()
		}
	}
	// A version marker alone does not establish that the expected schema exists.
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(runtime_intents)`)
	if err != nil {
		return fail()
	}
	expected := []string{"installation", "service", "operation", "resource", "fingerprint", "image", "revision", "phase", "observation", "version"}
	i := 0
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var defaultValue any
		if rows.Scan(&cid, &name, &typ, &notnull, &defaultValue, &pk) != nil || i >= len(expected) || cid != i || name != expected[i] || notnull != 1 || (i < 3 && pk != i+1) || (i >= 3 && pk != 0) {
			_ = rows.Close()
			return fail()
		}
		wantType := "TEXT"
		if i == 6 || i == 9 {
			wantType = "INTEGER"
		}
		if strings.ToUpper(typ) != wantType {
			_ = rows.Close()
			return fail()
		}
		i++
	}
	if rows.Err() != nil || rows.Close() != nil || i != len(expected) {
		return fail()
	}
	rows, err = db.QueryContext(ctx, `SELECT installation,service,operation,kind,phase,observation,version FROM runtime_actions LIMIT 0`)
	if err != nil {
		return fail()
	}
	if rows.Close() != nil {
		return fail()
	}
	return &Store{db: db}, nil
}
func (s *Store) Close() error { return s.db.Close() }
func valid(ref rt.Ref, revision uint64) bool {
	return ref.Valid() && fingerprint.MatchString(ref.Fingerprint) && ref.Revision == revision && (rt.Spec{Identity: ref.Identity, Image: ref.Image, Revision: revision}).Validate() == nil && revision <= math.MaxInt64
}
func observationValid(o reconcile.Observation) bool {
	switch o {
	case reconcile.Absent, reconcile.Created, reconcile.Running, reconcile.Stopped, reconcile.Conflict, reconcile.Unavailable, reconcile.Unknown:
		return true
	}
	return false
}

func validIntent(v Intent) bool {
	return valid(v.Ref, v.Revision) && v.Version > 0 && v.Version <= math.MaxInt64 && ((v.Phase == Prepared || v.Phase == Dispatched) && v.Observation == "" || v.Phase == Observed && observationValid(v.Observation))
}

type scanner interface{ Scan(...any) error }

func scanIntent(row scanner, id rt.Identity) (Intent, error) {
	v := Intent{}
	v.Ref.Identity = id
	if err := row.Scan(&v.Ref.ResourceID, &v.Ref.Fingerprint, &v.Ref.Image, &v.Revision, &v.Phase, &v.Observation, &v.Version); err != nil {
		return Intent{}, err
	}
	v.Ref.Revision = v.Revision
	if !validIntent(v) {
		return Intent{}, ErrUnavailable
	}
	return v, nil
}

// Prepare is idempotent only for the exact immutable intent. It never resets an
// existing phase, even when called after an uncertain dispatch.
func (s *Store) Prepare(ctx context.Context, ref rt.Ref, revision uint64) (Intent, error) {
	if !valid(ref, revision) {
		return Intent{}, ErrInvalid
	}
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO runtime_intents VALUES(?,?,?,?,?,?,?, 'prepared','',1)`, ref.Identity.Installation, ref.Identity.Service, ref.Identity.Operation, ref.ResourceID, ref.Fingerprint, ref.Image, revision)
	if err != nil {
		return Intent{}, ErrUnavailable
	}
	got, err := s.Get(ctx, ref.Identity)
	if err != nil {
		return Intent{}, err
	}
	if got.Ref != ref || got.Revision != revision {
		return Intent{}, ErrConflict
	}
	return got, nil
}

// Get returns persisted state, without contacting or mutating a runtime.
func (s *Store) Get(ctx context.Context, id rt.Identity) (Intent, error) {
	if !id.Valid() {
		return Intent{}, ErrInvalid
	}
	v, err := scanIntent(s.db.QueryRowContext(ctx, `SELECT resource,fingerprint,image,revision,phase,observation,version FROM runtime_intents WHERE installation=? AND service=? AND operation=?`, id.Installation, id.Service, id.Operation), id)
	if errors.Is(err, sql.ErrNoRows) {
		return Intent{}, ErrNotFound
	}
	if err != nil {
		return Intent{}, ErrUnavailable
	}
	return v, nil
}

// ListRecoverable enumerates dispatched and observed intentions with keyset
// pagination. Concurrent inserts before the cursor are visited on a later pass.
func (s *Store) ListRecoverable(ctx context.Context, after *rt.Identity, limit int) ([]Intent, error) {
	if limit < 1 || limit > 100 || (after != nil && !after.Valid()) {
		return nil, ErrInvalid
	}
	cursor := rt.Identity{}
	if after != nil {
		cursor = *after
	}
	rows, err := s.db.QueryContext(ctx, `SELECT installation,service,operation,resource,fingerprint,image,revision,phase,observation,version FROM runtime_intents WHERE phase IN ('dispatched','observed') AND (installation,service,operation) > (?,?,?) ORDER BY installation,service,operation LIMIT ?`, cursor.Installation, cursor.Service, cursor.Operation, limit)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer rows.Close()
	items := make([]Intent, 0, limit)
	for rows.Next() {
		var v Intent
		if rows.Scan(&v.Ref.Identity.Installation, &v.Ref.Identity.Service, &v.Ref.Identity.Operation, &v.Ref.ResourceID, &v.Ref.Fingerprint, &v.Ref.Image, &v.Revision, &v.Phase, &v.Observation, &v.Version) != nil {
			return nil, ErrUnavailable
		}
		v.Ref.Revision = v.Revision
		if !validIntent(v) {
			return nil, ErrUnavailable
		}
		items = append(items, v)
	}
	if rows.Err() != nil {
		return nil, ErrUnavailable
	}
	return items, nil
}

// MarkDispatched commits the uncertain phase before the caller sends a request.
// It may be called only once and a restart must not dispatch again automatically.
func (s *Store) MarkDispatched(ctx context.Context, id rt.Identity, version uint64) (Intent, error) {
	return s.transition(ctx, id, version, Prepared, Dispatched, "")
}

// RecordObservation records a read-only inspection after dispatch. Observations
// including absence do not authorize re-dispatch. A later observation may replace
// an earlier one only with its current optimistic version.
func (s *Store) RecordObservation(ctx context.Context, id rt.Identity, version uint64, from Phase, observation reconcile.Observation) (Intent, error) {
	if (from != Dispatched && from != Observed) || !observationValid(observation) {
		return Intent{}, ErrInvalid
	}
	return s.transition(ctx, id, version, from, Observed, observation)
}
func (s *Store) transition(ctx context.Context, id rt.Identity, version uint64, from, to Phase, observation reconcile.Observation) (Intent, error) {
	if !id.Valid() || version == 0 || version >= math.MaxInt64 {
		return Intent{}, ErrInvalid
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return Intent{}, ErrUnavailable
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return Intent{}, ErrUnavailable
	}
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	v, err := scanIntent(conn.QueryRowContext(ctx, `SELECT resource,fingerprint,image,revision,phase,observation,version FROM runtime_intents WHERE installation=? AND service=? AND operation=?`, id.Installation, id.Service, id.Operation), id)
	if errors.Is(err, sql.ErrNoRows) {
		return Intent{}, ErrConflict
	}
	if err != nil {
		return Intent{}, ErrUnavailable
	}
	if v.Phase != from || v.Version != version {
		return Intent{}, ErrConflict
	}
	result, err := conn.ExecContext(ctx, `UPDATE runtime_intents SET phase=?,observation=?,version=version+1 WHERE installation=? AND service=? AND operation=? AND phase=? AND version=?`, to, observation, id.Installation, id.Service, id.Operation, from, version)
	if err != nil {
		return Intent{}, ErrUnavailable
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return Intent{}, ErrUnavailable
	}
	v.Phase, v.Observation, v.Version = to, observation, version+1
	if !validIntent(v) {
		return Intent{}, ErrUnavailable
	}
	if _, err = conn.ExecContext(ctx, `COMMIT`); err != nil {
		return Intent{}, ErrUnavailable
	}
	return v, nil
}
