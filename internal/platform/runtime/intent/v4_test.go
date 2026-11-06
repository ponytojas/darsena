package intent

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	rt "github.com/ponytojas/darsena/internal/platform/runtime"
)

func TestV2MigrationReadsExistingRowsAsIsolated(t *testing.T) {
	ctx := context.Background()
	path := testPath(t)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE runtime_intents (
 installation TEXT NOT NULL, service TEXT NOT NULL, operation TEXT NOT NULL,
 resource TEXT NOT NULL, fingerprint TEXT NOT NULL, image TEXT NOT NULL, revision INTEGER NOT NULL CHECK(revision > 0),
 phase TEXT NOT NULL, observation TEXT NOT NULL, version INTEGER NOT NULL CHECK(version > 0), PRIMARY KEY(installation,service,operation));
 CREATE TABLE runtime_actions (
 installation TEXT NOT NULL, service TEXT NOT NULL, operation TEXT NOT NULL,
 kind TEXT NOT NULL, phase TEXT NOT NULL, observation TEXT NOT NULL, version INTEGER NOT NULL,
 PRIMARY KEY(installation,service,operation,kind)); PRAGMA user_version=2;`)
	if err != nil {
		t.Fatal(err)
	}
	ref := testRef()
	if _, err = db.Exec(`INSERT INTO runtime_intents VALUES(?,?,?,?,?,?,?,'observed','running',3)`, ref.Identity.Installation, ref.Identity.Service, ref.Identity.Operation, ref.ResourceID, ref.Fingerprint, ref.Image, ref.Revision); err != nil {
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
	v, err := s.Get(ctx, ref.Identity)
	if err != nil || v.Ref != ref || v.Ref.Access != rt.AccessIsolated || v.Version != 3 {
		t.Fatalf("migrated row: %+v %v", v, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	// Reopening a migrated store is a no-op.
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
}

func TestV4RefRoundTripsAndRejectsBadCombinations(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, testPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ref := testRef()
	ref.Access, ref.Port, ref.Project, ref.Shared, ref.Alias = rt.AccessLocal, 8080, "proj-1", true, "web"
	got, err := s.Prepare(ctx, ref, 1)
	if err != nil || got.Ref != ref {
		t.Fatalf("prepare: %+v %v", got, err)
	}
	again, err := s.Get(ctx, ref.Identity)
	if err != nil || again.Ref != ref {
		t.Fatalf("get: %+v %v", again, err)
	}
	listed, err := s.ListRecoverable(ctx, nil, 10)
	if err != nil || len(listed) != 0 {
		t.Fatalf("prepared intents are not recoverable: %v %v", listed, err)
	}
	if _, err = s.MarkDispatched(ctx, ref.Identity, 1); err != nil {
		t.Fatal(err)
	}
	listed, err = s.ListRecoverable(ctx, nil, 10)
	if err != nil || len(listed) != 1 || listed[0].Ref != ref {
		t.Fatalf("list: %+v %v", listed, err)
	}
	summaries, err := s.ListServices(ctx, ref.Identity.Installation, ref.Identity.Operation, []string{ref.Identity.Service})
	if err != nil || summaries[ref.Identity.Service].Invalid || summaries[ref.Identity.Service].Intent.Ref != ref {
		t.Fatalf("services: %+v %v", summaries, err)
	}
	// The same operation with a different access is a conflict, never a rewrite.
	other := ref
	other.Access = rt.AccessInternal
	if _, err = s.Prepare(ctx, other, 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("different access: %v", err)
	}
	for name, mutate := range map[string]func(*rt.Ref){
		"port on isolated":   func(r *rt.Ref) { r.Access, r.Port = rt.AccessIsolated, 80 },
		"local without port": func(r *rt.Ref) { r.Port = 0 },
		"unknown access":     func(r *rt.Ref) { r.Access = "open" },
		"missing alias":      func(r *rt.Ref) { r.Alias = "" },
		"bad project":        func(r *rt.Ref) { r.Project = strings.Repeat("a", 80) },
	} {
		bad := testRef()
		bad.Identity.Service = "other"
		bad.Access, bad.Port, bad.Alias = rt.AccessLocal, 80, "web"
		mutate(&bad)
		if _, err = s.Prepare(ctx, bad, 1); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}
