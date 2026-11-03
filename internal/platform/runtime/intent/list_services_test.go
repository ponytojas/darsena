package intent

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"sync/atomic"
	"testing"

	rt "github.com/ponytojas/darsena/internal/platform/runtime"
	"github.com/ponytojas/darsena/internal/platform/runtime/reconcile"
	"modernc.org/sqlite"
)

// countingConn counts statements prepared on it; without QueryerContext,
// database/sql prepares one statement per query.
type countingConn struct {
	driver.Conn
	n *atomic.Int64
}

func (c countingConn) Prepare(q string) (driver.Stmt, error) {
	c.n.Add(1)
	return c.Conn.Prepare(q)
}

type pathConnector struct {
	path string
	n    *atomic.Int64
}

func (c pathConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := (&sqlite.Driver{}).Open(c.path)
	if err != nil {
		return nil, err
	}
	return countingConn{conn, c.n}, nil
}
func (c pathConnector) Driver() driver.Driver { return &sqlite.Driver{} }

func serviceRef(service string) rt.Ref {
	r := testRef()
	r.Identity = rt.Identity{Installation: "install", Service: service, Operation: "initial"}
	return r
}

func TestListServicesFixedQueriesAndActions(t *testing.T) {
	ctx := context.Background()
	path := testPath(t)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ids := []string{}
	for i := 0; i < 12; i++ {
		id := fmt.Sprintf("app-%02d", i)
		ids = append(ids, id)
		if i%2 == 1 {
			continue // no intention for odd ones
		}
		ref := serviceRef(id)
		v, e := s.Prepare(ctx, ref, 1)
		if e != nil {
			t.Fatal(e)
		}
		v, e = s.MarkDispatched(ctx, ref.Identity, v.Version)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.RecordObservation(ctx, ref.Identity, v.Version, v.Phase, reconcile.Running); e != nil {
			t.Fatal(e)
		}
	}
	// app-00: stop and remove rows, the remove is preferred. app-02: only a
	// stop. app-04: an invalid action row (dispatched with an observation).
	for _, row := range []string{
		`('install','app-00','initial','stop','observed','stopped',2)`,
		`('install','app-00','initial','remove','dispatched','',1)`,
		`('install','app-02','initial','stop','observed','stopped',2)`,
		`('install','app-04','initial','stop','dispatched','running',1)`,
	} {
		if _, err = s.db.ExecContext(ctx, `INSERT INTO runtime_actions VALUES `+row); err != nil {
			t.Fatal(err)
		}
	}
	// Invalid intention row (bad fingerprint) for app-08.
	if _, err = s.db.ExecContext(ctx, `UPDATE runtime_intents SET fingerprint='x' WHERE service='app-08'`); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListServices(ctx, "install", "initial", ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 6 {
		t.Fatal(len(got), got)
	}
	if a := got["app-00"].Action; a == nil || a.Kind != "remove" || a.Phase != Dispatched {
		t.Fatalf("remove must win: %+v", a)
	}
	if a := got["app-02"].Action; a == nil || a.Kind != "stop" || a.Observation != reconcile.Stopped {
		t.Fatalf("%+v", a)
	}
	if a := got["app-04"].Action; a == nil || a.Kind != "unknown" {
		t.Fatalf("invalid action must be unknown: %+v", a)
	}
	if got["app-06"].Action != nil || got["app-06"].Invalid || got["app-06"].Intent.Version == 0 {
		t.Fatalf("%+v", got["app-06"])
	}
	if !got["app-08"].Invalid {
		t.Fatal("invalid intention not flagged")
	}
	// Same answer as the single-service view.
	for _, id := range []string{"app-00", "app-02", "app-04"} {
		single, e := s.LastAction(ctx, serviceRef(id).Identity)
		a := got[id].Action
		if e == nil && (a.Kind != single.Kind || a.Phase != single.Phase || a.Observation != single.Observation) {
			t.Fatal(id, a, single)
		}
		if e != nil && a.Kind != "unknown" {
			t.Fatal(id, e, a)
		}
	}
	if only, e := s.ListServices(ctx, "install", "initial", []string{"app-02"}); e != nil || len(only) != 1 {
		t.Fatal(only, e)
	}
	if other, e := s.ListServices(ctx, "other", "initial", ids); e != nil || len(other) != 0 {
		t.Fatal(other, e)
	}
	_ = s.Close()

	// The number of statements does not depend on the number of applications.
	var n atomic.Int64
	counted := &Store{db: sql.OpenDB(pathConnector{path, &n})}
	for _, in := range [][]string{ids[:1], ids} {
		n.Store(0)
		if _, e := counted.ListServices(ctx, "install", "initial", in); e != nil {
			t.Fatal(e)
		}
		if n.Load() != 2 {
			t.Fatalf("%d apps: %d statements", len(in), n.Load())
		}
	}
	_ = counted.Close()
	if _, e := counted.ListServices(ctx, "install", "initial", ids); e != ErrUnavailable {
		t.Fatal("closed store must be unavailable", e)
	}
}
