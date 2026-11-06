package jobs

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
)

func local(domain string) AppAccess { return AppAccess{Access: AccessLocal, Domain: domain} }

func countDomain(t *testing.T, s *Store, domain string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow("SELECT count(*) FROM applications WHERE domain=?", domain).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMigrateVersionThreeToFour(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(privateTempDir(t), "state.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE jobs (id TEXT PRIMARY KEY,resource_id TEXT NOT NULL,kind TEXT NOT NULL,idempotency_key TEXT NOT NULL UNIQUE,status TEXT NOT NULL,error TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,updated_at TEXT NOT NULL);
CREATE TABLE projects (id TEXT PRIMARY KEY, name TEXT NOT NULL, name_key TEXT NOT NULL UNIQUE, color TEXT NOT NULL, version INTEGER NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE applications (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, image TEXT NOT NULL, port INTEGER NOT NULL CHECK(port BETWEEN 1 AND 65535), created_at TEXT NOT NULL, project_id TEXT REFERENCES projects(id) ON DELETE SET NULL, version INTEGER NOT NULL DEFAULT 1, updated_at TEXT, runtime_used INTEGER NOT NULL DEFAULT 0);
INSERT INTO applications(id,name,image,port,created_at,updated_at,runtime_used,version) VALUES ('old1','old-one','` + testImage + `',80,'2026-01-01T00:00:00Z','2026-01-02T00:00:00Z',1,4),('old2','old-two','` + testImage + `',81,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z',0,1);
PRAGMA user_version=3;`)
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
	var version int
	if err = s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 4 {
		t.Fatal(version, err)
	}
	apps, err := s.Applications(ctx)
	if err != nil || len(apps) != 2 {
		t.Fatal(apps, err)
	}
	for _, a := range apps {
		if a.Access != AccessIsolated || a.SharedNetwork || a.Domain != nil {
			t.Fatalf("migrated application is not isolated: %+v", a)
		}
	}
	if apps[0].Version != 4 || !apps[0].ServiceUsed || apps[0].Port != 80 {
		t.Fatalf("existing data changed: %+v", apps[0])
	}
	// Two migrated rows with NULL domains do not collide, and the index works on them.
	if _, err = s.UpdateApplication(ctx, "old1", 4, testImage, 80, &AppAccess{Access: AccessLocal, Domain: "mine"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.UpdateApplication(ctx, "old2", 1, testImage, 81, &AppAccess{Access: AccessLocal, Domain: "mine"}); !errors.Is(err, ErrDomainTaken) {
		t.Fatal(err)
	}
	// Opening an already migrated database changes nothing.
	s.Close()
	again, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if a, err := again.Application(ctx, "old1"); err != nil || a.Domain == nil || *a.Domain != "mine" {
		t.Fatal(a, err)
	}
}

func TestDomainFormatAndReserved(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	n := 0
	create := func(a AppAccess) error {
		n++
		_, err := s.CreateApplication(ctx, fmt.Sprintf("app-%d", n), testImage, 80, a)
		return err
	}
	for _, d := range []string{"ab", "a-b", "mi-web", "a1", strings.Repeat("a", 40), "MAYUS"} {
		if err := create(local(d)); err != nil {
			t.Fatalf("%q rejected: %v", d, err)
		}
	}
	for _, d := range []string{"", "a", "1abc", "abc-", "-abc", "a_b", "a.b", "ab c", "ñandú", "a--", strings.Repeat("a", 41), "a\n", " ab"} {
		if err := create(local(d)); !errors.Is(err, ErrInvalidDomain) {
			t.Fatalf("%q: %v", d, err)
		}
	}
	for _, d := range []string{"localhost", "www", "api", "darsena", "admin", "ADMIN", "Localhost"} {
		if err := create(local(d)); !errors.Is(err, ErrReservedDomain) || errors.Is(err, ErrInvalidDomain) {
			t.Fatalf("%q: %v", d, err)
		}
	}
	// A domain belongs to local applications only.
	for _, a := range []AppAccess{{Domain: "web"}, {Access: AccessIsolated, Domain: "web"}, {Access: AccessInternal, Domain: "web"}} {
		if err := create(a); !errors.Is(err, ErrInvalidDomain) {
			t.Fatalf("%+v: %v", a, err)
		}
	}
	if err := create(AppAccess{Access: AccessIsolated, SharedNetwork: true}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if err := create(AppAccess{Access: "open"}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	for _, a := range []AppAccess{{}, {Access: AccessInternal, SharedNetwork: true}} {
		if err := create(a); err != nil {
			t.Fatal(err)
		}
	}
	var stored int
	if err := s.db.QueryRow("SELECT count(*) FROM applications WHERE access!='local' AND domain IS NOT NULL").Scan(&stored); err != nil || stored != 0 {
		t.Fatal("a non-local application stored a domain", stored, err)
	}
	apps, _ := s.Applications(ctx)
	if len(apps) != 8 {
		t.Fatalf("rejected definitions left rows: %d", len(apps))
	}
}

func TestDomainUniquenessCaseAndLifecycle(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	a, err := s.CreateApplication(ctx, "app-a", testImage, 80, local("Mi-Web"))
	if err != nil || a.Access != AccessLocal || a.Domain == nil || *a.Domain != "mi-web" {
		t.Fatal(a, err)
	}
	if _, err = s.CreateApplication(ctx, "app-b", testImage, 80, local("mi-WEB")); !errors.Is(err, ErrDomainTaken) {
		t.Fatal(err)
	}
	if _, err = s.CreateApplication(ctx, "app-a", testImage, 80, local("other")); !errors.Is(err, ErrConflict) {
		t.Fatalf("name conflict: %v", err)
	}
	b, err := s.CreateApplication(ctx, "app-b", testImage, 80, local("other"))
	if err != nil {
		t.Fatal(err)
	}
	// Taking another application's domain is rejected and leaves the row as it was.
	if _, err = s.UpdateApplication(ctx, b.ID, 1, testImage, 80, &AppAccess{Access: AccessLocal, Domain: "MI-WEB"}); !errors.Is(err, ErrDomainTaken) {
		t.Fatal(err)
	}
	if got, _ := s.Application(ctx, b.ID); got.Version != 1 || *got.Domain != "other" {
		t.Fatalf("failed update changed the row: %+v", got)
	}
	// A bad edit does not either.
	for _, bad := range []AppAccess{local("www"), local("x"), {Access: AccessInternal, Domain: "other"}} {
		if _, err = s.UpdateApplication(ctx, b.ID, 1, testImage, 80, &bad); err == nil {
			t.Fatalf("%+v accepted", bad)
		}
	}
	if got, _ := s.Application(ctx, b.ID); got.Version != 1 {
		t.Fatalf("rejected edit changed the row: %+v", got)
	}
	// Editing the image alone keeps access and domain; re-saving one's own domain is fine.
	up, err := s.UpdateApplication(ctx, a.ID, 1, testImage, 9000, nil)
	if err != nil || up.Access != AccessLocal || *up.Domain != "mi-web" || up.Port != 9000 {
		t.Fatal(up, err)
	}
	if up, err = s.UpdateApplication(ctx, a.ID, 2, testImage, 9000, &AppAccess{Access: AccessLocal, Domain: "mi-web", SharedNetwork: true}); err != nil || !up.SharedNetwork {
		t.Fatal(up, err)
	}
	// Changing the domain frees the old one.
	if up, err = s.UpdateApplication(ctx, a.ID, 3, testImage, 9000, &AppAccess{Access: AccessLocal, Domain: "nuevo"}); err != nil || *up.Domain != "nuevo" {
		t.Fatal(up, err)
	}
	if _, err = s.CreateApplication(ctx, "app-c", testImage, 80, local("mi-web")); err != nil {
		t.Fatalf("freed domain not reusable: %v", err)
	}
	// Leaving local clears it, and the stored value is NULL.
	if up, err = s.UpdateApplication(ctx, a.ID, 4, testImage, 9000, &AppAccess{Access: AccessInternal}); err != nil || up.Domain != nil || up.Access != AccessInternal {
		t.Fatal(up, err)
	}
	if countDomain(t, s, "nuevo") != 0 {
		t.Fatal("domain survived leaving local")
	}
	if _, err = s.CreateApplication(ctx, "app-d", testImage, 80, local("nuevo")); err != nil {
		t.Fatalf("domain not freed by access change: %v", err)
	}
	// Deleting frees it too.
	if err = s.DeleteApplication(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateApplication(ctx, "app-e", testImage, 80, local("other")); err != nil {
		t.Fatalf("domain not freed by delete: %v", err)
	}
	// Going back to local needs a domain again.
	if _, err = s.UpdateApplication(ctx, a.ID, 5, testImage, 9000, &AppAccess{Access: AccessLocal}); !errors.Is(err, ErrInvalidDomain) {
		t.Fatal(err)
	}
}

func TestConcurrentCreatesOfOneDomainLeaveExactlyOne(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	// More connections than the default pool, so writers genuinely interleave.
	s.db.SetMaxOpenConns(8)
	const n = 24
	variants := []string{"Race", "race", "RACE", "rAcE"}
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = s.CreateApplication(ctx, fmt.Sprintf("app-%d", i), testImage, 80, local(variants[i%len(variants)]))
		}(i)
	}
	close(start)
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrDomainTaken):
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != 1 || countDomain(t, s, "race") != 1 {
		t.Fatalf("winners %d, rows %d", ok, countDomain(t, s, "race"))
	}
	apps, _ := s.Applications(ctx)
	if len(apps) != 1 {
		t.Fatalf("losers left rows: %d", len(apps))
	}
}

func TestConcurrentEditsToOneDomainLeaveExactlyOne(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	s.db.SetMaxOpenConns(8)
	const n = 16
	ids := make([]string, n)
	for i := range ids {
		a, err := s.CreateApplication(ctx, fmt.Sprintf("app-%d", i), testImage, 80, local(fmt.Sprintf("own-%d", i)))
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = a.ID
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, n)
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = s.UpdateApplication(ctx, ids[i], 1, testImage, 80, &AppAccess{Access: AccessLocal, Domain: []string{"Target", "target"}[i%2]})
		}(i)
	}
	close(start)
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrDomainTaken):
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != 1 || countDomain(t, s, "target") != 1 {
		t.Fatalf("winners %d, rows %d", ok, countDomain(t, s, "target"))
	}
	// Every loser still holds its own domain.
	held := 0
	for i := range ids {
		a, _ := s.Application(ctx, ids[i])
		if *a.Domain == fmt.Sprintf("own-%d", i) {
			held++
		}
	}
	if held != n-1 {
		t.Fatalf("losers lost their domain: %d of %d", held, n-1)
	}
}
