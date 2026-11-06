package configvault

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
)

const leakA, leakB = "TOP-SECRET-ALPHA-9f3", "TOP-SECRET-BETA-77c"

func openTest(t *testing.T) (*Vault, string, *age.X25519Identity) {
	t.Helper()
	id, _ := age.GenerateX25519Identity()
	path := filepath.Join(privateDir(t), "config.db")
	v, err := Open(context.Background(), path, id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { v.Close() })
	return v, path, id
}

func mustJSON(t *testing.T, x any) string {
	t.Helper()
	b, err := json.Marshal(x)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// legacyVault builds a version 0 database (no dates) with one revision.
func legacyVault(t *testing.T, path string, id *age.X25519Identity) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v := &Vault{identity: id}
	sentinel, _ := v.encrypt([]byte("darsena-config-v1"))
	raw, _ := json.Marshal(document{Resource: "app", Revision: 1, Entries: map[string]entry{"PLAIN": {"p", false}, "KEY": {leakA, true}}})
	ct, _ := v.encrypt(raw)
	for _, q := range []string{
		"CREATE TABLE metadata (name TEXT PRIMARY KEY,value BLOB NOT NULL)",
		"CREATE TABLE configurations (resource TEXT PRIMARY KEY,revision INTEGER NOT NULL,ciphertext BLOB NOT NULL)",
		"CREATE TABLE configuration_history (resource TEXT NOT NULL,revision INTEGER NOT NULL,ciphertext BLOB NOT NULL,PRIMARY KEY(resource,revision))",
	} {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec("INSERT INTO metadata VALUES('identity_check',?)", sentinel); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO configurations VALUES('app',1,?)", ct); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO configuration_history VALUES('app',1,?)", ct); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationFromUnversionedAndRejectNewer(t *testing.T) {
	ctx := context.Background()
	id, _ := age.GenerateX25519Identity()
	path := filepath.Join(privateDir(t), "config.db")
	legacyVault(t, path, id)
	v, err := Open(ctx, path, id)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := v.Get(ctx, "app")
	if err != nil || snap.Revision != 1 || len(snap.Variables) != 2 {
		t.Fatal(snap, err)
	}
	revs, err := v.Revisions(ctx)
	if err != nil || revs["app"].Revision != 1 || revs["app"].UpdatedAt != nil {
		t.Fatal(revs, err)
	}
	h, err := v.HistoryEntries(ctx, "app")
	if err != nil || len(h) != 1 || h[0].CreatedAt != nil || !h[0].Base {
		t.Fatal(h, err)
	}
	if _, err = v.Set(ctx, "app", 1, "PLAIN", "q", false); err != nil {
		t.Fatal(err)
	}
	revs, _ = v.Revisions(ctx)
	if revs["app"].UpdatedAt == nil {
		t.Fatal("save must fill updated_at")
	}
	var version int
	if v.db.QueryRow("PRAGMA user_version").Scan(&version) != nil || version != 1 {
		t.Fatal(version)
	}
	// Legacy secret rewritten with an equal value is indistinguishable from untouched;
	// the new revision touches only PLAIN.
	h, _ = v.HistoryEntries(ctx, "app")
	if len(h) != 2 || len(h[1].Changes) != 1 || h[1].Changes[0].Name != "PLAIN" {
		t.Fatal(mustJSON(t, h))
	}
	if h[1].CreatedAt == nil {
		t.Fatal("created_at missing")
	}
	v.Close()
	db, _ := sql.Open("sqlite", path)
	if _, err = db.Exec("PRAGMA user_version=2"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err = Open(ctx, path, id); err == nil {
		t.Fatal("version 2 must be rejected")
	}
}

func TestHistoryEntriesClassificationAndNoLeak(t *testing.T) {
	ctx := context.Background()
	v, _, _ := openTest(t)
	steps := []func(rev int64) error{
		func(r int64) error { _, e := v.Set(ctx, "app", r, "KEY", leakA, true); return e },
		func(r int64) error { _, e := v.Set(ctx, "app", r, "PLAIN", "one", false); return e },
		func(r int64) error { _, e := v.Set(ctx, "app", r, "KEY", leakA, true); return e },    // same secret value
		func(r int64) error { _, e := v.Set(ctx, "app", r, "PLAIN", "one", false); return e }, // same plain value
		func(r int64) error { _, e := v.Set(ctx, "app", r, "PLAIN", "two", false); return e },
		func(r int64) error { _, e := v.Set(ctx, "app", r, "KEY", leakB, true); return e },
		func(r int64) error { _, e := v.Delete(ctx, "app", r, "KEY"); return e },
	}
	for i, s := range steps {
		if err := s(int64(i)); err != nil {
			t.Fatal(i, err)
		}
	}
	h, err := v.HistoryEntries(ctx, "app")
	if err != nil || len(h) != 7 {
		t.Fatal(len(h), err)
	}
	want := [][]string{
		{"KEY:added"}, {"PLAIN:added"}, {"KEY:changed"}, {}, {"PLAIN:changed"}, {"KEY:changed"}, {"KEY:removed"},
	}
	for i, e := range h {
		var got []string
		for _, c := range e.Changes {
			got = append(got, c.Name+":"+c.Kind)
		}
		if strings.Join(got, ",") != strings.Join(want[i], ",") {
			t.Errorf("rev %d: got %v want %v", e.Revision, got, want[i])
		}
	}
	if !h[0].Base || h[1].Base {
		t.Fatal("base flag")
	}
	out := mustJSON(t, h)
	if strings.Contains(out, leakA) || strings.Contains(out, leakB) {
		t.Fatal("history leaked a value: " + out)
	}
	if strings.Contains(out, `"one"`) || strings.Contains(out, `"two"`) {
		t.Fatal("history leaked a plain value")
	}
	if _, err = v.HistoryEntries(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestHistoryEntriesDetectsIntegrityMismatch(t *testing.T) {
	ctx := context.Background()
	v, _, _ := openTest(t)
	v.Set(ctx, "app", 0, "A", "x", false)
	v.Set(ctx, "app", 1, "A", "y", false)
	var ct []byte
	v.db.QueryRow("SELECT ciphertext FROM configuration_history WHERE revision=2").Scan(&ct)
	v.db.Exec("UPDATE configuration_history SET ciphertext=? WHERE revision=1", ct)
	if _, err := v.HistoryEntries(ctx, "app"); err == nil {
		t.Fatal("expected integrity error")
	}
}

func TestRestoreRules(t *testing.T) {
	ctx := context.Background()
	v, _, _ := openTest(t)
	v.Set(ctx, "app", 0, "KEY", leakA, true)
	v.Set(ctx, "app", 1, "KEY", leakB, true)
	v.Set(ctx, "app", 2, "PLAIN", "plain-old", false)
	v.Set(ctx, "app", 3, "PLAIN", "plain-new", false)
	if _, err := v.Restore(ctx, "app", 3, "KEY", 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale: %v", err)
	}
	if _, err := v.Restore(ctx, "app", 4, "KEY", 99); !errors.Is(err, ErrNotRetained) {
		t.Fatalf("missing revision: %v", err)
	}
	if _, err := v.Restore(ctx, "app", 4, "PLAIN", 1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("absent variable: %v", err)
	}
	res, err := v.Restore(ctx, "app", 4, "KEY", 1)
	if err != nil || res.Revision != 5 || !res.Secret {
		t.Fatal(res, err)
	}
	if s := mustJSON(t, res); strings.Contains(s, leakA) || strings.Contains(s, leakB) {
		t.Fatal(s)
	}
	acc, _ := v.Accept(ctx, "app", 5)
	got := ""
	acc.Visit(func(n, val string, _ bool) {
		if n == "KEY" {
			got = val
		}
	})
	if got != leakA {
		t.Fatal("value not restored")
	}
	res, err = v.Restore(ctx, "app", 5, "PLAIN", 3)
	if err != nil || res.Secret || res.Revision != 6 {
		t.Fatal(res, err)
	}
	if strings.Contains(mustJSON(t, res), "plain-old") {
		t.Fatal("plain value leaked")
	}
	h, _ := v.HistoryEntries(ctx, "app")
	if last := h[len(h)-1]; len(last.Changes) != 1 || last.Changes[0].Name != "PLAIN" {
		t.Fatal(mustJSON(t, last))
	}
	for i := int64(6); i < 30; i++ {
		v.Set(ctx, "app", i, "Z", "z", false)
	}
	if _, err = v.Restore(ctx, "app", 30, "KEY", 1); !errors.Is(err, ErrNotRetained) {
		t.Fatalf("pruned: %v", err)
	}
	if _, err = v.Restore(ctx, "none", 0, "KEY", 1); !errors.Is(err, ErrNotRetained) {
		t.Fatalf("no config: %v", err)
	}
}

func TestPurgeAndRevisions(t *testing.T) {
	ctx := context.Background()
	v, _, _ := openTest(t)
	v.Set(ctx, "a", 0, "K", "1", true)
	v.Set(ctx, "a", 1, "K", "2", true)
	v.Set(ctx, "b", 0, "K", "1", true)
	revs, err := v.Revisions(ctx)
	if err != nil || len(revs) != 2 || revs["a"].Revision != 2 || revs["a"].UpdatedAt == nil {
		t.Fatal(revs, err)
	}
	if s := mustJSON(t, revs); strings.Contains(s, `"K"`) {
		t.Fatal(s)
	}
	if err = v.Purge(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	var n int
	v.db.QueryRow("SELECT count(*) FROM configuration_history WHERE resource='a'").Scan(&n)
	if n != 0 {
		t.Fatal("history rows remain")
	}
	if h, _ := v.History(ctx, "b"); len(h) != 1 {
		t.Fatal("other resource affected")
	}
	if _, err = v.HistoryEntries(ctx, "a"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	var sd int
	v.db.QueryRow("PRAGMA secure_delete").Scan(&sd)
	if sd != 1 {
		t.Fatal("secure_delete off")
	}
}
