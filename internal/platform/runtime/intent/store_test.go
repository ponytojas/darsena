package intent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	rt "github.com/ponytojas/darsena/internal/platform/runtime"
	"github.com/ponytojas/darsena/internal/platform/runtime/reconcile"
)

func testPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "db")
}

func TestCorruptTransitionRollsBack(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, testPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ref := testRef()
	if _, err = s.Prepare(ctx, ref, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE runtime_intents SET fingerprint='bad-secret'`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.MarkDispatched(ctx, ref.Identity, 1); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("dispatch corrupt: %v", err)
	}
	var phase string
	var version int
	if err = s.db.QueryRowContext(ctx, `SELECT phase,version FROM runtime_intents`).Scan(&phase, &version); err != nil || phase != "prepared" || version != 1 {
		t.Fatalf("changed corrupt row: %s %d %v", phase, version, err)
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE runtime_intents SET fingerprint=?,phase='dispatched'`, ref.Fingerprint); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE runtime_intents SET fingerprint='bad-secret'`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RecordObservation(ctx, ref.Identity, 1, Dispatched, reconcile.Running); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("observe corrupt: %v", err)
	}
	if err = s.db.QueryRowContext(ctx, `SELECT phase,version FROM runtime_intents`).Scan(&phase, &version); err != nil || phase != "dispatched" || version != 1 {
		t.Fatalf("changed corrupt row: %s %d %v", phase, version, err)
	}
}

func TestRecoverablePaginationAndCorruption(t *testing.T) {
	ctx := context.Background()
	path := testPath(t)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 205; i++ {
		ref := testRef()
		ref.Identity.Operation = fmt.Sprintf("operation-%03d", i)
		v, e := s.Prepare(ctx, ref, 1)
		if e != nil {
			t.Fatal(e)
		}
		if i != 103 {
			if _, e = s.MarkDispatched(ctx, ref.Identity, v.Version); e != nil {
				t.Fatal(e)
			}
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var cursor *rt.Identity
	count := 0
	previous := ""
	for {
		page, e := s.ListRecoverable(ctx, cursor, 100)
		if e != nil {
			t.Fatal(e)
		}
		for _, v := range page {
			if v.Ref.Identity.Operation <= previous || v.Phase == Prepared {
				t.Fatalf("bad page: %+v", v)
			}
			previous = v.Ref.Identity.Operation
			count++
		}
		if len(page) < 100 {
			break
		}
		id := page[len(page)-1].Ref.Identity
		cursor = &id
	}
	if count != 204 {
		t.Fatalf("count %d", count)
	}
	if _, err = s.ListRecoverable(ctx, nil, 101); !errors.Is(err, ErrInvalid) {
		t.Fatalf("limit: %v", err)
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE runtime_intents SET fingerprint='secret-marker' WHERE operation='operation-001'`); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListRecoverable(ctx, nil, 100)
	if !errors.Is(err, ErrUnavailable) || page != nil || strings.Contains(err.Error(), "secret-marker") {
		t.Fatalf("corrupt page: %v %v", page, err)
	}
}

func TestRejectIncompleteVersionedSchema(t *testing.T) {
	path := testPath(t)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE runtime_intents (installation TEXT NOT NULL); PRAGMA user_version=1`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Open(context.Background(), path); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("incomplete schema: %v", err)
	}
}

func TestConcurrentDispatchAcrossConnections(t *testing.T) {
	ctx := context.Background()
	path := testPath(t)
	s1, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s1.Close()
	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	ref := testRef()
	if _, err = s1.Prepare(ctx, ref, 1); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, s := range []*Store{s1, s2} {
		go func(s *Store) { <-start; _, e := s.MarkDispatched(ctx, ref.Identity, 1); results <- e }(s)
	}
	close(start)
	a, b := <-results, <-results
	if !((a == nil && errors.Is(b, ErrConflict)) || (b == nil && errors.Is(a, ErrConflict))) {
		t.Fatalf("results %v %v", a, b)
	}
}
func testRef() rt.Ref {
	return rt.Ref{Identity: rt.Identity{Installation: "install", Service: "service", Operation: "operation"}, ResourceID: "resource", Fingerprint: strings.Repeat("a", 64), Image: "registry.example/app@sha256:" + strings.Repeat("b", 64), Revision: 1}
}
func TestPersistenceAndNoRetry(t *testing.T) {
	ctx := context.Background()
	path := testPath(t)
	ref := testRef()
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := s.Prepare(ctx, ref, 1)
	if err != nil || initial.Phase != Prepared || initial.Version != 1 {
		t.Fatalf("prepare: %+v %v", initial, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.Get(ctx, ref.Identity)
	if err != nil || current != initial {
		t.Fatalf("reopen prepared: %+v %v", current, err)
	}
	dispatched, err := s.MarkDispatched(ctx, ref.Identity, current.Version)
	if err != nil || dispatched.Phase != Dispatched {
		t.Fatalf("dispatch: %+v %v", dispatched, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	current, err = s.Prepare(ctx, ref, 1)
	if err != nil || current != dispatched {
		t.Fatalf("reprepare reset phase: %+v %v", current, err)
	}
	if _, err = s.MarkDispatched(ctx, ref.Identity, current.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("repeat dispatch: %v", err)
	}
	observed, err := s.RecordObservation(ctx, ref.Identity, current.Version, Dispatched, reconcile.Absent)
	if err != nil || observed.Phase != Observed || observed.Observation != reconcile.Absent {
		t.Fatalf("observe: %+v %v", observed, err)
	}
	if _, err = s.MarkDispatched(ctx, ref.Identity, observed.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("absence authorized retry: %v", err)
	}
	if _, err = s.RecordObservation(ctx, ref.Identity, observed.Version, Observed, reconcile.Running); err != nil {
		t.Fatalf("refresh: %v", err)
	}
}
func TestConflictsAndValidation(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, testPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ref := testRef()
	if _, err = s.Prepare(ctx, ref, 1); err != nil {
		t.Fatal(err)
	}
	changed := ref
	changed.Fingerprint = strings.Repeat("b", 64)
	if _, err = s.Prepare(ctx, changed, 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed fingerprint: %v", err)
	}
	changedRevision := ref
	changedRevision.Revision = 3
	if _, err = s.Prepare(ctx, changedRevision, 3); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed revision: %v", err)
	}
	if _, err = s.RecordObservation(ctx, ref.Identity, 1, Prepared, reconcile.Absent); !errors.Is(err, ErrInvalid) {
		t.Fatalf("premature observation: %v", err)
	}
	if _, err = s.RecordObservation(ctx, ref.Identity, 1, Dispatched, "sensitive value"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid observation: %v", err)
	}
	ref.Fingerprint = "secret"
	if _, err = s.Prepare(ctx, ref, 1); !errors.Is(err, ErrInvalid) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("invalid fingerprint: %v", err)
	}
}
func TestConcurrentDispatchCAS(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, testPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ref := testRef()
	if _, err = s.Prepare(ctx, ref, 1); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := s.MarkDispatched(ctx, ref.Identity, 1); results <- e }()
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for e := range results {
		if e == nil {
			success++
		} else if errors.Is(e, ErrConflict) {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
}

func TestRejectUnsafeFilesAndCorruptState(t *testing.T) {
	ctx := context.Background()
	path := testPath(t)
	if err := os.WriteFile(path+"-wal", []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path+"-wal", 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, path); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unsafe sidecar: %v", err)
	}
	if err := os.Remove(path + "-wal"); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ref := testRef()
	if _, err = s.Prepare(ctx, ref, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE runtime_intents SET fingerprint=?`, "secret-marker"); err != nil {
		t.Fatal(err)
	}
	_, err = s.Get(ctx, ref.Identity)
	if !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "secret-marker") {
		t.Fatalf("corrupt row: %v", err)
	}
}

func TestHasServiceSeesPreparedIntentions(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, testPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ref := testRef()
	if has, err := s.HasService(ctx, ref.Identity.Service); err != nil || has {
		t.Fatal(has, err)
	}
	if _, err = s.Prepare(ctx, ref, 1); err != nil {
		t.Fatal(err)
	}
	if has, err := s.HasService(ctx, ref.Identity.Service); err != nil || !has {
		t.Fatal(has, err)
	}
	if has, err := s.HasService(ctx, "other"); err != nil || has {
		t.Fatal(has, err)
	}
}
