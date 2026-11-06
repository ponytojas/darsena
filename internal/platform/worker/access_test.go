package worker

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	rt "github.com/ponytojas/darsena/internal/platform/runtime"
)

func (h *imageHarness) doAny(method, path, body string) (int, map[string]any) {
	h.t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.s.mux.ServeHTTP(rec, req)
	out := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func (h *imageHarness) newApp(name, access, domain string, shared bool) (int, map[string]any) {
	body := fmt.Sprintf(`{"name":%q,"image":"docker.io/library/nginx@sha256:%s","port":8080,"access":%q,"shared_network":%t,"domain":%q}`, name, hexA, access, shared, domain)
	return h.doAny("POST", "/applications", body)
}

func (h *imageHarness) mustApp(name, access, domain string) string {
	h.t.Helper()
	code, out := h.newApp(name, access, domain, false)
	if code != 201 {
		h.t.Fatalf("create %s: %d %v", name, code, out)
	}
	return out["id"].(string)
}

func TestIsolatedSpecIsUnchanged(t *testing.T) {
	h := newImageHarness(t, true)
	id := h.mustApp("aislada", "", "")
	if code, body := h.doAny("POST", "/applications/"+id+"/service", `{"revision":0}`); code != 201 && code != 202 {
		t.Fatalf("%d %v", code, body)
	}
	want := rt.Spec{Identity: rt.Identity{Installation: "install", Service: id, Operation: "initial"}, Image: "docker.io/library/nginx@sha256:" + hexA, Revision: 1}
	if h.provider.lastSpec != want {
		t.Fatalf("spec %+v", h.provider.lastSpec)
	}
	if h.provider.ensures.Load() != 0 {
		t.Fatal("isolated services need no network")
	}
	_, view := h.doAny("GET", "/applications/"+id+"/service", "")
	if view["access"] != "isolated" || view["domain"] != nil {
		t.Fatalf("%v", view)
	}
	if _, has := view["host_port"]; has {
		t.Fatal("host port exposed")
	}
}

func TestInternalAndLocalSpecs(t *testing.T) {
	h := newImageHarness(t, true)
	_, p := h.doAny("POST", "/projects", `{"name":"uno","color":"blue"}`)
	project := p["id"].(string)
	internal := h.mustApp("interna", "internal", "")
	local := h.mustApp("abierta", "local", "")
	if code, body := h.doAny("POST", "/applications/assign", fmt.Sprintf(`{"project_id":%q,"application_ids":[%q]}`, project, internal)); code != 200 {
		t.Fatalf("%d %v", code, body)
	}
	cur, _ := h.s.store.Application(context.Background(), internal)
	if code, body := h.doAny("PATCH", "/applications/"+internal, fmt.Sprintf(`{"version":%d,`, cur.Version)+`"image":"docker.io/library/nginx@sha256:`+hexA+`","port":8080,"shared_network":true}`); code != 200 {
		t.Fatalf("shared: %d %v", code, body)
	}
	img := "docker.io/library/nginx@sha256:" + hexA
	if code, body := h.doAny("POST", "/applications/"+internal+"/service", `{"revision":0}`); code != 201 && code != 202 {
		t.Fatalf("%d %v", code, body)
	}
	want := rt.Spec{Identity: rt.Identity{Installation: "install", Service: internal, Operation: "initial"}, Image: img, Revision: 1, Access: "internal", Port: 8080, Project: project, Shared: true, Alias: "interna"}
	if h.provider.lastSpec != want {
		t.Fatalf("internal spec %+v", h.provider.lastSpec)
	}
	if code, body := h.doAny("POST", "/applications/"+local+"/service", `{"revision":0}`); code != 201 && code != 202 {
		t.Fatalf("%d %v", code, body)
	}
	want = rt.Spec{Identity: rt.Identity{Installation: "install", Service: local, Operation: "initial"}, Image: img, Revision: 1, Access: "local", Port: 8080, Alias: "abierta"}
	if h.provider.lastSpec != want {
		t.Fatalf("local spec %+v", h.provider.lastSpec)
	}
	if len(h.provider.ensured) != 2 {
		t.Fatalf("ensured %d", len(h.provider.ensured))
	}
	_, view := h.doAny("GET", "/applications/"+local+"/service", "")
	if view["access"] != "local" || view["domain"] != "abierta" {
		t.Fatalf("%v", view)
	}
	// A repeat with a changed project is a conflict, not a second create.
	creates := h.provider.creates.Load()
	if code, _ := h.doAny("POST", "/applications/assign", fmt.Sprintf(`{"project_id":%q,"application_ids":[%q]}`, project, local)); code != 200 {
		t.Fatal(code)
	}
	if code, body := h.doAny("POST", "/applications/"+local+"/service", `{"revision":1}`); code != 409 {
		t.Fatalf("moved project: %d %v", code, body)
	}
	if h.provider.creates.Load() != creates {
		t.Fatal("second create")
	}
}

func TestNetworkFailureSpendsNothing(t *testing.T) {
	ctx := context.Background()
	h := newImageHarness(t, true)
	id := h.mustApp("abierta", "local", "")
	path := "/applications/" + id + "/service"
	for _, c := range []struct {
		err        error
		code       int
		wantStatus string
	}{{rt.ErrNetworkUnavailable, 503, "network_unavailable"}, {rt.ErrConflict, 409, "network_conflict"}, {rt.ErrUnavailable, 503, "runtime_unavailable"}} {
		h.provider.networkErr = c.err
		if code, body := h.doAny("POST", path, `{"revision":0}`); code != c.code || body["code"] != c.wantStatus {
			t.Fatalf("%v: %d %v", c.err, code, body)
		}
		if got, _ := h.s.store.Application(ctx, id); got.ServiceUsed {
			t.Fatal("marked before dispatch")
		}
		if snap, _ := h.vault.Get(ctx, id); snap.Revision != 0 {
			t.Fatal("configuration initialized")
		}
		if code, _ := h.doAny("GET", path, ""); code != 404 {
			t.Fatalf("intention reserved: %d", code)
		}
		if h.provider.creates.Load() != 0 {
			t.Fatal("dispatched")
		}
	}
	h.provider.networkErr = nil
	if code, body := h.doAny("POST", path, `{"revision":0}`); code != 201 && code != 202 {
		t.Fatalf("retry: %d %v", code, body)
	}
}

func TestRejectedDomainSpendsNothing(t *testing.T) {
	ctx := context.Background()
	h := newImageHarness(t, true)
	id := h.mustApp("abierta", "local", "")
	db, err := sql.Open("sqlite", filepath.Join(h.dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("UPDATE applications SET domain=NULL WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	if code, body := h.doAny("POST", "/applications/"+id+"/service", `{"revision":0}`); code != 400 || body["code"] != "invalid_domain" {
		t.Fatalf("%d %v", code, body)
	}
	if got, _ := h.s.store.Application(ctx, id); got.ServiceUsed || h.provider.ensures.Load() != 0 || h.provider.creates.Load() != 0 {
		t.Fatal("something was spent")
	}
}

func TestApplicationAccessErrors(t *testing.T) {
	h := newImageHarness(t, true)
	h.mustApp("primera", "local", "web")
	cases := []struct {
		name, access, domain string
		code                 int
		want                 string
	}{
		{"segunda", "local", "WEB", 409, "domain_taken"},
		{"tercera", "local", "www", 400, "domain_reserved"},
		{"cuarta", "local", "bad_domain", 400, "invalid_domain"},
		{"quinta", "internal", "algo", 400, "invalid_domain"},
		{"sexta", "bogus", "", 400, "invalid_application"},
		{"septima-", "local", "", 400, "invalid_domain"},
	}
	for _, c := range cases {
		if code, body := h.newApp(c.name, c.access, c.domain, false); code != c.code || body["code"] != c.want {
			t.Errorf("%s: %d %v", c.name, code, body)
		}
	}
	// A local application without a domain defaults to its name.
	if code, body := h.newApp("sin-dominio", "local", "", false); code != 201 || body["domain"] != "sin-dominio" {
		t.Fatalf("default domain: %d %v", code, body)
	}
}

func TestConcurrentSameDomainHasOneWinner(t *testing.T) {
	h := newImageHarness(t, true)
	var wg sync.WaitGroup
	codes := make([]int, 8)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := fmt.Sprintf(`{"name":"app-%d","image":"docker.io/library/nginx@sha256:%s","port":80,"access":"local","domain":"mismo"}`, i, hexA)
			req := httptest.NewRequest("POST", "/applications", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			h.s.mux.ServeHTTP(rec, req)
			codes[i] = rec.Code
			if rec.Code == 409 && !strings.Contains(rec.Body.String(), "domain_taken") {
				t.Errorf("loser: %s", rec.Body)
			}
		}()
	}
	wg.Wait()
	wins := 0
	for _, c := range codes {
		if c == 201 {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("codes %v", codes)
	}
}

func TestPatchAccessAndLock(t *testing.T) {
	h := newImageHarness(t, true)
	img := "docker.io/library/nginx@sha256:" + hexA
	id := h.mustApp("editable", "internal", "")
	h.mustApp("otra", "local", "")
	patch := func(version int, extra string) (int, map[string]any) {
		return h.doAny("PATCH", "/applications/"+id, fmt.Sprintf(`{"version":%d,"image":%q,"port":8080%s}`, version, img, extra))
	}
	// Going local defaults the domain to the name; leaving clears it.
	if code, body := patch(1, `,"access":"local"`); code != 200 || body["domain"] != "editable" {
		t.Fatalf("%d %v", code, body)
	}
	if code, body := patch(2, `,"domain":"otra"`); code != 409 || body["code"] != "domain_taken" {
		t.Fatalf("%d %v", code, body)
	}
	if code, body := patch(2, `,"domain":"www"`); code != 400 || body["code"] != "domain_reserved" {
		t.Fatalf("%d %v", code, body)
	}
	if code, body := patch(2, `,"access":"internal"`); code != 200 || body["domain"] != nil {
		t.Fatalf("%d %v", code, body)
	}
	if code, body := h.doAny("GET", "/domains/check?name=editable", ""); code != 200 || body["available"] != true {
		t.Fatalf("freed: %d %v", code, body)
	}
	// Omitting the access fields keeps them.
	if code, body := patch(3, ``); code != 200 || body["access"] != "internal" {
		t.Fatalf("%d %v", code, body)
	}
	if code, body := patch(4, `,"access":"local","domain":"editable"`); code != 200 {
		t.Fatalf("%d %v", code, body)
	}

	if code, _ := h.doAny("POST", "/applications/"+id+"/service", `{"revision":0}`); code != 201 && code != 202 {
		t.Fatal(code)
	}
	version := 5
	for _, extra := range []string{`,"access":"internal"`, `,"access":"isolated"`, `,"shared_network":true`, `,"domain":"nuevo"`} {
		if code, body := patch(version, extra); code != 409 || body["code"] != "access_locked" {
			t.Fatalf("%s: %d %v", extra, code, body)
		}
	}
	if code, body := h.doAny("PATCH", "/applications/"+id, fmt.Sprintf(`{"version":%d,"image":%q,"port":9090}`, version, img)); code != 409 || body["code"] != "access_locked" {
		t.Fatalf("port: %d %v", code, body)
	}
	// Saving the same values is not a change.
	if code, body := patch(version, `,"access":"local","domain":"EDITABLE","shared_network":false`); code != 200 {
		t.Fatalf("same values: %d %v", code, body)
	}
	// An isolated application with a service may still change its (unused) port.
	iso := h.mustApp("aislada", "", "")
	if code, _ := h.doAny("POST", "/applications/"+iso+"/service", `{"revision":0}`); code != 201 && code != 202 {
		t.Fatal(code)
	}
	if code, body := h.doAny("PATCH", "/applications/"+iso, fmt.Sprintf(`{"version":1,"image":%q,"port":9090}`, img)); code != 200 {
		t.Fatalf("isolated port: %d %v", code, body)
	}
}

func TestDomainCheck(t *testing.T) {
	h := newImageHarness(t, false)
	id := h.mustApp("mia", "local", "mi-web")
	for _, c := range []struct {
		query     string
		available bool
		reason    string
	}{
		{"name=libre", true, ""},
		{"name=mi-web", false, "taken"},
		{"name=MI-WEB", false, "taken"},
		{"name=mi-web&application=" + id, true, ""},
		{"name=mi-web&application=otro", false, "taken"},
		{"name=www", false, "reserved"},
		{"name=a", false, "invalid"},
		{"name=", false, "invalid"},
		{"name=bad.name", false, "invalid"},
	} {
		code, body := h.doAny("GET", "/domains/check?"+c.query, "")
		reason, _ := body["reason"].(string)
		if code != 200 || body["available"] != c.available || reason != c.reason {
			t.Errorf("%s: %d %v", c.query, code, body)
		}
	}
}

func TestRoutes(t *testing.T) {
	h := newImageHarness(t, true)
	get := func(domain string) (int, map[string]any) { return h.doAny("GET", "/routes/"+domain, "") }
	if code, _ := get("nadie"); code != 404 {
		t.Fatalf("unknown: %d", code)
	}
	if code, _ := get("Bad_Name"); code != 404 {
		t.Fatalf("invalid: %d", code)
	}
	// An application without a service is not running.
	local := h.mustApp("abierta", "local", "")
	if code, body := get("abierta"); code != 503 || body["code"] != "not_running" {
		t.Fatalf("no service: %d %v", code, body)
	}
	// Isolated and internal applications have no domain: unknown.
	h.mustApp("aislada", "", "")
	if code, _ := get("aislada"); code != 404 {
		t.Fatalf("isolated: %d", code)
	}
	if code, _ := h.doAny("POST", "/applications/"+local+"/service", `{"revision":0}`); code != 201 && code != 202 {
		t.Fatal(code)
	}
	h.provider.result.HostPort = 32771
	_, first := h.doAny("GET", "/applications/"+local+"/service", "")
	if code, body := get("abierta"); code != 200 || body["host_port"] != float64(32771) {
		t.Fatalf("running: %d %v", code, body)
	}
	// Reading a route records no observation.
	_, view := h.doAny("GET", "/applications/"+local+"/service", "")
	if view["version"] != first["version"] || view["observation"] != first["observation"] {
		t.Fatalf("route changed the intention: %v -> %v", first, view)
	}
	h.provider.result.State = rt.StateStopped
	if code, body := get("abierta"); code != 503 || body["code"] != "not_running" {
		t.Fatalf("stopped: %d %v", code, body)
	}
	h.provider.result.State = rt.StateRunning
	h.provider.inspectErr = rt.ErrUnavailable
	if code, body := get("abierta"); code != 503 || body["code"] != "route_unavailable" {
		t.Fatalf("inspect failure: %d %v", code, body)
	}
}

func TestInfoPolicyAndExposedPort(t *testing.T) {
	h := newImageHarness(t, true)
	if code, body := h.doAny("GET", "/runtime/info", ""); code != 200 || body["policy"] != "dockerlocal-v4" {
		t.Fatalf("%d %v", code, body)
	}
	h.provider.exposedPort = 8080
	code, body := h.doAny("GET", "/image?ref=docker.io/library/nginx@sha256:"+hexA, "")
	if code != 200 || body["exposed_port"] != float64(8080) {
		t.Fatalf("%d %v", code, body)
	}
}
