package intent

import (
	"context"
	"database/sql"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ponytojas/darsena/internal/platform/runtime/reconcile"
)

func TestV1MigrationPreservesIntent(t *testing.T) {
	ctx := context.Background()
	path := testPath(t)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE runtime_intents (
 installation TEXT NOT NULL, service TEXT NOT NULL, operation TEXT NOT NULL,
 resource TEXT NOT NULL, fingerprint TEXT NOT NULL, image TEXT NOT NULL, revision INTEGER NOT NULL,
 phase TEXT NOT NULL, observation TEXT NOT NULL, version INTEGER NOT NULL,
 PRIMARY KEY(installation,service,operation)); PRAGMA user_version=1;`)
	if err != nil {
		t.Fatal(err)
	}
	ref := testRef()
	_, err = db.Exec(`INSERT INTO runtime_intents VALUES(?,?,?,?,?,?,?,'observed','running',3)`, ref.Identity.Installation, ref.Identity.Service, ref.Identity.Operation, ref.ResourceID, ref.Fingerprint, ref.Image, ref.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	v, err := s.Get(ctx, ref.Identity)
	if err != nil || v.Ref != ref || v.Version != 3 || v.Observation != reconcile.Running {
		t.Fatalf("migration: %+v %v", v, err)
	}
	_, _, send, err := s.DispatchAction(ctx, ref.Identity, "stop", v.Version)
	if err != nil || !send {
		t.Fatalf("dispatch: %v %v", send, err)
	}
}

func TestActionCASAndRestart(t *testing.T) {
	ctx := context.Background()
	path := testPath(t)
	a, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ref := testRef()
	v, err := a.Prepare(ctx, ref, 1)
	if err != nil {
		t.Fatal(err)
	}
	v, err = a.MarkDispatched(ctx, ref.Identity, v.Version)
	if err != nil {
		t.Fatal(err)
	}
	v, err = a.RecordObservation(ctx, ref.Identity, v.Version, v.Phase, reconcile.Running)
	if err != nil {
		t.Fatal(err)
	}
	var sends atomic.Int32
	var wg sync.WaitGroup
	for _, s := range []*Store{a, b} {
		wg.Add(1)
		go func(s *Store) {
			defer wg.Done()
			_, _, send, e := s.DispatchAction(ctx, ref.Identity, "stop", v.Version)
			if e != nil {
				t.Errorf("dispatch: %v", e)
			}
			if send {
				sends.Add(1)
			}
		}(s)
	}
	wg.Wait()
	if sends.Load() != 1 {
		t.Fatalf("got %d dispatches", sends.Load())
	}
	if err = a.Close(); err != nil {
		t.Fatal(err)
	}
	a, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	item, act, send, err := a.DispatchAction(ctx, ref.Identity, "stop", v.Version)
	if err != nil || send || act.Phase != Dispatched {
		t.Fatalf("restart %+v %v %v", act, send, err)
	}
	if _, _, send, err = a.DispatchAction(ctx, ref.Identity, "remove", item.Version); err != ErrConflict || send {
		t.Fatalf("remove before stop %v %v", send, err)
	}
	item, act, err = a.ObserveAction(ctx, ref.Identity, "stop", act.Version, item.Version, reconcile.Stopped)
	if err != nil {
		t.Fatal(err)
	}
	item, act, send, err = a.DispatchAction(ctx, ref.Identity, "remove", item.Version)
	if err != nil || !send {
		t.Fatalf("remove %v %v", send, err)
	}
	item, act, err = a.ObserveAction(ctx, ref.Identity, "remove", act.Version, item.Version, reconcile.Absent)
	if err != nil || item.Observation != reconcile.Absent {
		t.Fatalf("observation %+v %v", item, err)
	}
	_, _, send, err = a.DispatchAction(ctx, ref.Identity, "remove", item.Version)
	if err != nil || send {
		t.Fatalf("remove replay %v %v", send, err)
	}
}
