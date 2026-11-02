package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
}

func (p *testProvider) Check(context.Context) error { return p.checkErr }

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
