package worker

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"filippo.io/age"
	"github.com/ponytojas/darsena/internal/platform/configvault"
	rt "github.com/ponytojas/darsena/internal/platform/runtime"
	"github.com/ponytojas/darsena/internal/platform/runtime/dockerlocal"
	"github.com/ponytojas/darsena/internal/platform/runtime/intent"
	"github.com/ponytojas/darsena/internal/platform/runtime/reconcile"
)

type testProvider struct {
	creates    atomic.Int32
	inspects   atomic.Int32
	stops      atomic.Int32
	removes    atomic.Int32
	result     rt.Result
	fail       bool
	stopLost   bool
	removeLost bool
	checkErr   error
	// image is the availability ImageStatus reports; empty means available.
	imageMu  sync.Mutex
	image    rt.ImageState
	imageErr error
	pullErr  error
	pulls    atomic.Int32
	pullGate chan struct{}
	// inspectErr, when set, is returned by Inspect.
	inspectErr error
}

func (p *testProvider) Check(context.Context) error { return p.checkErr }
func (p *testProvider) ImageStatus(ctx context.Context, pinned string) (rt.ImageStatus, error) {
	if p.imageErr != nil {
		return rt.ImageStatus{}, p.imageErr
	}
	p.imageMu.Lock()
	state := p.image
	p.imageMu.Unlock()
	if state == "" {
		state = rt.ImageAvailable
	}
	out := rt.ImageStatus{State: state, DaemonPlatform: "linux/arm64"}
	if state == rt.ImageIncompatible {
		out.Reason, out.Platform = "platform", "windows/amd64"
	}
	return out, nil
}
func (p *testProvider) ResolveTag(ctx context.Context, tagged string) (string, error) {
	if strings.Contains(tagged, "private") {
		return "", rt.ErrNotFound
	}
	return "sha256:" + strings.Repeat("d", 64), nil
}
func (p *testProvider) RegistrySupports(ctx context.Context, pinned string) (bool, string, error) {
	if strings.Contains(pinned, "nanoserver") {
		return false, "windows/amd64", nil
	}
	return true, "linux/arm64", nil
}
func (p *testProvider) Pull(ctx context.Context, pinned string) error {
	p.pulls.Add(1)
	if p.pullGate != nil {
		<-p.pullGate
	}
	if p.pullErr == nil {
		p.imageMu.Lock()
		p.image = rt.ImageAvailable
		p.imageMu.Unlock()
	}
	return p.pullErr
}

func (p *testProvider) Stop(ctx context.Context, r rt.Ref) (rt.Result, error) {
	p.stops.Add(1)
	if p.result.Ref != r {
		return rt.Result{}, rt.ErrNotFound
	}
	p.result.State = rt.StateStopped
	if p.stopLost {
		return rt.Result{}, rt.ErrUnknownOutcome
	}
	return p.result, nil
}
func (p *testProvider) Remove(ctx context.Context, r rt.Ref) error {
	p.removes.Add(1)
	if p.result.Ref == r {
		p.result = rt.Result{}
	}
	if p.removeLost {
		return rt.ErrUnknownOutcome
	}
	return nil
}

func (p *testProvider) CreateConfigured(ctx context.Context, s rt.Spec, a configvault.Accepted) (rt.Result, error) {
	p.creates.Add(1)
	ref, _ := dockerlocal.PrepareRef(s)
	p.result = rt.Result{Ref: ref, State: rt.StateRunning}
	if p.fail {
		return rt.Result{}, rt.ErrUnknownOutcome
	}
	return p.result, nil
}
func (p *testProvider) Inspect(ctx context.Context, r rt.Ref) (rt.Result, error) {
	p.inspects.Add(1)
	if p.inspectErr != nil {
		return rt.Result{}, p.inspectErr
	}
	if p.result.Ref != r {
		return rt.Result{}, rt.ErrNotFound
	}
	return p.result, nil
}
func TestExperimentalServiceLifecycle(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, filepath.Join(dir, "state.db"), filepath.Join(dir, "socket"))
	if err != nil {
		t.Fatal(err)
	}
	key, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	vault, err := configvault.Open(ctx, filepath.Join(dir, "vault.db"), key)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.EnableConfig(vault); err != nil {
		t.Fatal(err)
	}
	store, err := intent.Open(ctx, filepath.Join(dir, "intent.db"))
	if err != nil {
		t.Fatal(err)
	}
	provider := &testProvider{fail: true}
	if err = s.EnableRuntime("install", store, dockerlocal.PrepareRef, provider); err != nil {
		t.Fatal(err)
	}
	app, err := s.store.CreateApplication(ctx, "my-app", "example.com/image@sha256:"+strings.Repeat("a", 64), 3000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = vault.Set(ctx, app.ID, 0, "SECRET", "sensitive-marker", true); err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, req)
		if strings.Contains(rec.Body.String(), "sensitive-marker") {
			t.Fatal("secret disclosed")
		}
		return rec
	}
	path := "/applications/" + app.ID + "/service"
	if got := request("POST", path, `{"revision":2,"acknowledge_no_network":true}`); got.Code != 409 || !strings.Contains(got.Body.String(), `"code":"revision_conflict_before_dispatch"`) {
		t.Fatalf("stale: %d %s", got.Code, got.Body)
	}
	if provider.creates.Load() != 0 {
		t.Fatal("stale revision sent")
	}
	if got := request("GET", path, ""); got.Code != 404 {
		t.Fatalf("stale revision reserved an intention: %d", got.Code)
	}
	if got := request("POST", path, `{"revision":1,"acknowledge_no_network":false}`); got.Code != 400 {
		t.Fatal(got.Code)
	}
	created := request("POST", path, `{"revision":1,"acknowledge_no_network":true}`)
	if created.Code != 202 {
		t.Fatalf("create: %d %s", created.Code, created.Body)
	}
	var view serviceResponse
	if err := json.Unmarshal(created.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Observation != "unknown" || view.Phase != intent.Observed {
		t.Fatalf("unexpected %+v", view)
	}
	if _, err = vault.Set(ctx, app.ID, 1, "OTHER", "value", false); err != nil {
		t.Fatal(err)
	}
	repeated := request("POST", path, `{"revision":1,"acknowledge_no_network":true}`)
	if repeated.Code != 200 || provider.creates.Load() != 1 {
		t.Fatalf("repeat %d creates %d", repeated.Code, provider.creates.Load())
	}
	if got := request("POST", path, `{"revision":2,"acknowledge_no_network":true}`); got.Code != 409 || strings.Contains(got.Body.String(), "revision_conflict_before_dispatch") {
		t.Fatalf("new revision %d", got.Code)
	}
	refreshed := request("POST", path+"/refresh", "")
	if refreshed.Code != 200 {
		t.Fatalf("refresh %d %s", refreshed.Code, refreshed.Body)
	}
	if err = json.Unmarshal(refreshed.Body.Bytes(), &view); err != nil || view.Observation != "running" {
		t.Fatalf("refresh %+v %v", view, err)
	}
	if provider.creates.Load() != 1 || provider.inspects.Load() != 1 {
		t.Fatal("unexpected provider calls")
	}
	// Restart retains the claim and never dispatches another create.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, filepath.Join(dir, "state.db"), filepath.Join(dir, "socket"))
	if err != nil {
		t.Fatal(err)
	}
	vault, err = configvault.Open(ctx, filepath.Join(dir, "vault.db"), key)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.EnableConfig(vault); err != nil {
		t.Fatal(err)
	}
	store, err = intent.Open(ctx, filepath.Join(dir, "intent.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.EnableRuntime("install", store, dockerlocal.PrepareRef, provider); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	req := httptest.NewRequest("POST", path, bytes.NewBufferString(`{"revision":1,"acknowledge_no_network":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != 200 || provider.creates.Load() != 1 {
		t.Fatalf("restart replay: %d, %d", rec.Code, provider.creates.Load())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	stale := request("POST", path+"/stop", fmt.Sprintf(`{"version":%d}`, view.Version+1))
	if stale.Code != 409 || provider.stops.Load() != 0 {
		t.Fatalf("stale stop: %d", stale.Code)
	}
	provider.stopLost = true
	stopped := request("POST", path+"/stop", fmt.Sprintf(`{"version":%d}`, view.Version))
	if stopped.Code != 202 {
		t.Fatalf("stop: %d %s", stopped.Code, stopped.Body)
	}
	if err := json.Unmarshal(stopped.Body.Bytes(), &view); err != nil || view.Observation != reconcile.Unknown || view.Action == nil {
		t.Fatalf("stop result %+v %v", view, err)
	}
	replay := request("POST", path+"/stop", fmt.Sprintf(`{"version":%d}`, view.Version))
	if replay.Code != 200 || provider.stops.Load() != 1 {
		t.Fatalf("stop replay %d count %d", replay.Code, provider.stops.Load())
	}
	blocked := request("POST", path+"/remove", fmt.Sprintf(`{"version":%d,"acknowledge_remove":true}`, view.Version))
	if blocked.Code != 409 || provider.removes.Load() != 0 {
		t.Fatalf("remove while uncertain: %d", blocked.Code)
	}
	refreshed = request("POST", path+"/refresh", "")
	if err := json.Unmarshal(refreshed.Body.Bytes(), &view); refreshed.Code != 200 || err != nil || view.Observation != reconcile.Stopped {
		t.Fatalf("reconcile: %d %+v %v", refreshed.Code, view, err)
	}
	provider.removeLost = true
	removed := request("POST", path+"/remove", fmt.Sprintf(`{"version":%d,"acknowledge_remove":true}`, view.Version))
	if removed.Code != 202 {
		t.Fatalf("remove: %d %s", removed.Code, removed.Body)
	}
	if err := json.Unmarshal(removed.Body.Bytes(), &view); err != nil || view.Observation != reconcile.Unknown {
		t.Fatalf("remove result %+v %v", view, err)
	}
	replay = request("POST", path+"/remove", fmt.Sprintf(`{"version":%d,"acknowledge_remove":true}`, view.Version))
	if replay.Code != 200 || provider.removes.Load() != 1 {
		t.Fatalf("remove replay %d count %d", replay.Code, provider.removes.Load())
	}
	refreshed = request("POST", path+"/refresh", "")
	if err := json.Unmarshal(refreshed.Body.Bytes(), &view); refreshed.Code != 200 || err != nil || view.Observation != reconcile.Absent {
		t.Fatalf("reconcile removal %d %+v %v", refreshed.Code, view, err)
	}
	if provider.removes.Load() != 1 || provider.creates.Load() != 1 {
		t.Fatal("reconciliation repeated a mutation")
	}
	info := request("GET", "/runtime/info", "")
	if info.Code != http.StatusOK || !strings.Contains(info.Body.String(), `"mode":"experimental"`) {
		t.Fatal(info.Body)
	}
	if ready := request("GET", "/runtime/ready", ""); ready.Code != 200 {
		t.Fatal(ready.Code)
	}
	provider.checkErr = errors.New("sensitive-marker")
	if ready := request("GET", "/runtime/ready", ""); ready.Code != 503 {
		t.Fatal(ready.Code)
	}
}
func TestExperimentalRuntimeDisabled(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := Open(context.Background(), filepath.Join(dir, "state"), filepath.Join(dir, "socket"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	req := httptest.NewRequest("POST", "/applications/any/service", bytes.NewBufferString(`{}`))
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatal(rec.Code)
	}
	if err = s.EnableRuntime("install", nil, nil, nil); err == nil {
		t.Fatal(errors.New("runtime unexpectedly enabled"))
	}
	_ = fmt.Sprint(rec.Code)
}

func TestRuntimeGuardsApplicationEditAndDelete(t *testing.T) {
	ctx := context.Background()
	dir, err := os.MkdirTemp("/tmp", "darsena-guard-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	s, err := Open(ctx, filepath.Join(dir, "state.db"), filepath.Join(dir, "socket"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	key, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	vault, err := configvault.Open(ctx, filepath.Join(dir, "vault.db"), key)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.EnableConfig(vault); err != nil {
		t.Fatal(err)
	}
	store, err := intent.Open(ctx, filepath.Join(dir, "intent.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.EnableRuntime("install", store, dockerlocal.PrepareRef, &testProvider{}); err != nil {
		t.Fatal(err)
	}
	image := func(c string) string { return "example.com/image@sha256:" + strings.Repeat(c, 64) }
	plain, err := s.store.CreateApplication(ctx, "plain-app", image("a"), 3000)
	if err != nil {
		t.Fatal(err)
	}
	used, err := s.store.CreateApplication(ctx, "used-app", image("a"), 3000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = vault.Set(ctx, used.ID, 0, "A", "v", false); err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, req)
		return rec
	}
	expect := func(rec *httptest.ResponseRecorder, status int, code string) {
		t.Helper()
		if rec.Code != status || (code != "" && !strings.Contains(rec.Body.String(), `"code":"`+code+`"`)) {
			t.Fatalf("want %d %s, got %d %s", status, code, rec.Code, rec.Body)
		}
	}
	expect(request("POST", "/applications/"+used.ID+"/service", `{"revision":1,"acknowledge_no_network":true}`), 202, "")

	// An intention locks the image but not the port.
	expect(request("PATCH", "/applications/"+used.ID, `{"version":1,"image":"`+image("b")+`","port":3000}`), 409, "image_locked")
	expect(request("PATCH", "/applications/"+used.ID, `{"version":1,"image":"`+image("a")+`","port":4000}`), 200, "")
	expect(request("DELETE", "/applications/"+used.ID, ""), 409, "application_has_service")
	if _, err = s.store.Application(ctx, used.ID); err != nil {
		t.Fatal(err)
	}
	if snap, err := vault.Get(ctx, used.ID); err != nil || snap.Revision != 1 {
		t.Fatal("configuration of a deployed application was touched", snap, err)
	}

	// A busy gate is reported, never waited for.
	if !s.runtime.tryAcquire() {
		t.Fatal("gate unexpectedly busy")
	}
	expect(request("DELETE", "/applications/"+plain.ID, ""), 409, "runtime_busy")
	expect(request("PATCH", "/applications/"+plain.ID, `{"version":1,"image":"`+image("b")+`","port":3000}`), 409, "runtime_busy")
	expect(request("PATCH", "/applications/"+plain.ID, `{"version":1,"image":"`+image("a")+`","port":3001}`), 200, "")
	s.runtime.release()

	// Without an intention the image is editable and the definition deletable.
	expect(request("PATCH", "/applications/"+plain.ID, `{"version":2,"image":"`+image("b")+`","port":3001}`), 200, "")
	expect(request("DELETE", "/applications/"+plain.ID, ""), 204, "")
	if !s.runtime.tryAcquire() {
		t.Fatal("gate leaked")
	}
	s.runtime.release()
}

func TestCreateServiceMarksApplicationAndHandlesDeletion(t *testing.T) {
	ctx := context.Background()
	dir, err := os.MkdirTemp("/tmp", "darsena-mark-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	s, err := Open(ctx, filepath.Join(dir, "state.db"), filepath.Join(dir, "socket"))
	if err != nil {
		t.Fatal(err)
	}
	key, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	vault, err := configvault.Open(ctx, filepath.Join(dir, "vault.db"), key)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.EnableConfig(vault); err != nil {
		t.Fatal(err)
	}
	store, err := intent.Open(ctx, filepath.Join(dir, "intent.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.EnableRuntime("install", store, dockerlocal.PrepareRef, &testProvider{}); err != nil {
		t.Fatal(err)
	}
	image := "example.com/image@sha256:" + strings.Repeat("a", 64)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, req)
		return rec
	}
	app, err := s.store.CreateApplication(ctx, "marked-app", image, 3000)
	if err != nil {
		t.Fatal(err)
	}
	gone, err := s.store.CreateApplication(ctx, "gone-app", image, 3000)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []string{app.ID, gone.ID} {
		if _, err = vault.Set(ctx, a, 0, "A", "v", false); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := s.store.Application(ctx, app.ID); got.ServiceUsed {
		t.Fatal("fresh application marked")
	}
	if rec := request("POST", "/applications/"+app.ID+"/service", `{"revision":1,"acknowledge_no_network":true}`); rec.Code != 202 {
		t.Fatal(rec.Code, rec.Body)
	}
	if got, _ := s.store.Application(ctx, app.ID); !got.ServiceUsed {
		t.Fatal("create did not mark the application")
	}
	if rec := request("GET", "/applications/"+app.ID, ""); !strings.Contains(rec.Body.String(), `"service_used":true`) {
		t.Fatal(rec.Body)
	}

	// Deleted between the identity check and the gate: 404, not 500.
	s.beforeGate = func() { _ = s.store.DeleteApplication(ctx, gone.ID) }
	rec := request("POST", "/applications/"+gone.ID+"/service", `{"revision":1,"acknowledge_no_network":true}`)
	s.beforeGate = nil
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), `"code":"application_not_found"`) {
		t.Fatal(rec.Code, rec.Body)
	}
	if !s.runtime.tryAcquire() {
		t.Fatal("gate leaked")
	}
	s.runtime.release()

	// A worker without the runtime (demo) on the same state still refuses.
	demo, err := Open(ctx, filepath.Join(dir, "other.db"), filepath.Join(dir, "socket2"))
	if err != nil {
		t.Fatal(err)
	}
	defer demo.Close()
	marked, err := demo.store.CreateApplication(ctx, "demo-marked", image, 3000)
	if err != nil {
		t.Fatal(err)
	}
	if err = demo.store.MarkRuntimeUsed(ctx, marked.ID); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ method, body string }{{"DELETE", ""}, {"PATCH", `{"version":1,"image":"example.com/image@sha256:` + strings.Repeat("b", 64) + `","port":3000}`}} {
		req := httptest.NewRequest(c.method, "/applications/"+marked.ID, bytes.NewBufferString(c.body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		demo.mux.ServeHTTP(rec, req)
		// Without the runtime, absence of the container cannot be confirmed.
		want := "runtime_state_unknown"
		if c.method == "PATCH" {
			want = "image_locked"
		}
		if rec.Code != 409 || !strings.Contains(rec.Body.String(), want) {
			t.Fatal(c.method, rec.Code, rec.Body)
		}
	}
}

func TestApplicationSummary(t *testing.T) {
	ctx := context.Background()
	dir, err := os.MkdirTemp("/tmp", "darsena-sum-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	s, err := Open(ctx, filepath.Join(dir, "state.db"), filepath.Join(dir, "socket"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if strings.Contains(rec.Body.String(), "sensitive-marker") {
			t.Fatal("secret disclosed")
		}
		return rec
	}
	image := "example.com/image@sha256:" + strings.Repeat("a", 64)
	a, _ := s.store.CreateApplication(ctx, "app-a", image, 3000)
	b, _ := s.store.CreateApplication(ctx, "app-b", image, 3000)
	c, _ := s.store.CreateApplication(ctx, "app-c", image, 3000)
	type summary struct {
		ID                string `json:"id"`
		ProjectID         *string
		Version           int
		UpdatedAt         string `json:"updated_at"`
		ServiceUsed       bool   `json:"service_used"`
		Config            *struct{ Revision int }
		ConfigUnavailable bool `json:"config_unavailable"`
		Service           *struct {
			Phase   string
			Version int
			Action  *struct{ Kind string }
		}
		ServiceUnavailable bool `json:"service_unavailable"`
	}
	list := func() map[string]summary {
		rec := get("/applications")
		var all []summary
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &all) != nil {
			t.Fatal(rec.Code, rec.Body)
		}
		m := map[string]summary{}
		for _, x := range all {
			m[x.ID] = x
		}
		return m
	}

	// Without runtime and vault: plain fields, null config and service.
	m := list()
	if len(m) != 3 || m[a.ID].Version != 1 || m[a.ID].UpdatedAt == "" || m[a.ID].Config != nil || m[a.ID].Service != nil || m[a.ID].ServiceUnavailable {
		t.Fatalf("%+v", m[a.ID])
	}
	if !strings.Contains(get("/applications").Body.String(), `"service":null`) {
		t.Fatal("service must be an explicit null")
	}

	key, _ := age.GenerateX25519Identity()
	vault, err := configvault.Open(ctx, filepath.Join(dir, "vault.db"), key)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.EnableConfig(vault); err != nil {
		t.Fatal(err)
	}
	store, err := intent.Open(ctx, filepath.Join(dir, "intent.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.EnableRuntime("install", store, dockerlocal.PrepareRef, &testProvider{}); err != nil {
		t.Fatal(err)
	}
	if _, err = vault.Set(ctx, a.ID, 0, "TOKEN", "sensitive-marker", true); err != nil {
		t.Fatal(err)
	}
	if _, err = vault.Set(ctx, b.ID, 0, "A", "v", false); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/applications/"+a.ID+"/service", bytes.NewBufferString(`{"revision":1,"acknowledge_no_network":true}`))
	req.Header.Set("Content-Type", "application/json")
	if rec := httptest.NewRecorder(); func() int { s.mux.ServeHTTP(rec, req); return rec.Code }() != 202 {
		t.Fatal("create failed")
	}
	m = list()
	if m[a.ID].Config == nil || m[a.ID].Config.Revision != 1 || m[a.ID].Service == nil || m[a.ID].Service.Phase == "" || !m[a.ID].ServiceUsed {
		t.Fatalf("%+v", m[a.ID])
	}
	if m[b.ID].Config == nil || m[b.ID].Service != nil || m[c.ID].Config != nil || m[c.ID].Service != nil || m[c.ID].ServiceUnavailable {
		t.Fatalf("%+v %+v", m[b.ID], m[c.ID])
	}
	if rec := get("/applications/" + a.ID); !strings.Contains(rec.Body.String(), `"service":{"phase"`) {
		t.Fatal(rec.Body)
	}
	if rec := get("/applications/missing"); rec.Code != 404 {
		t.Fatal(rec.Code)
	}

	// An invalid action row is reported as unknown, not as a failure.
	db, err := sql.Open("sqlite", filepath.Join(dir, "intent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`INSERT INTO runtime_actions VALUES('install',?, 'initial','stop','dispatched','running',1)`, a.ID); err != nil {
		t.Fatal(err)
	}
	m = list()
	if m[a.ID].Service == nil || m[a.ID].Service.Action == nil || m[a.ID].Service.Action.Kind != "unknown" {
		t.Fatalf("%+v", m[a.ID].Service)
	}

	// A failing intention store degrades every item without failing the list.
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	m = list()
	for _, x := range m {
		if x.Service != nil || !x.ServiceUnavailable {
			t.Fatalf("%+v", x)
		}
	}
	if m[a.ID].Config == nil {
		t.Fatal("config must survive a service failure")
	}
}
