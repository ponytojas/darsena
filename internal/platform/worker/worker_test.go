package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"filippo.io/age"
	"fmt"
	"github.com/ponytojas/darsena/internal/modules/demo"
	"github.com/ponytojas/darsena/internal/platform/configvault"
	"github.com/ponytojas/darsena/internal/platform/jobs"
	"github.com/ponytojas/darsena/internal/transport"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSocketLifecycleAndPersistence(t *testing.T) {
	ctx := context.Background()
	dir, err := os.MkdirTemp("/tmp", "darsena-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(dir, "state.db")
	sock := filepath.Join(dir, "worker.sock")
	s, err := Open(ctx, db, sock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Open(ctx, db, filepath.Join(dir, "other.sock")); err == nil {
		t.Fatal("second worker acquired database")
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- s.Run(runCtx) }()
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}, Timeout: time.Second}
	post := func(body string) *http.Response {
		r, err := client.Post("http://worker/jobs", "application/json", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	invalid := post(`{"resource_id":"app","idempotency_key":"x"} {}`)
	if invalid.StatusCode != 400 {
		t.Fatal(invalid.StatusCode)
	}
	invalid.Body.Close()
	resp := post(`{"resource_id":"app","idempotency_key":"key"}`)
	var j Response
	if err = json.NewDecoder(resp.Body).Decode(&j); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 202 {
		t.Fatal(resp.StatusCode)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, err = client.Get("http://worker/jobs/" + j.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.NewDecoder(resp.Body).Decode(&j); err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if j.Status == "succeeded" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("job did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, db, sock)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.store.Get(ctx, j.ID)
	if err != nil || got.Status != "succeeded" {
		t.Fatal(got, err)
	}
	if info, err := os.Stat(sock); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("socket permissions", err)
	}
}
func TestRecoveryDoesNotRetryExternalEffects(t *testing.T) {
	ctx := context.Background()
	dir, err := os.MkdirTemp("/tmp", "darsena-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	os.Chmod(dir, 0700)
	db := filepath.Join(dir, "db")
	store, err := jobs.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	j, err := store.Enqueue(ctx, "app", "simulation", "key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ClaimNext(ctx); err != nil {
		t.Fatal(err)
	}
	store.Close()
	s, err := Open(ctx, db, filepath.Join(dir, "sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.store.Get(ctx, j.ID)
	if err != nil || got.Status != "interrupted" {
		t.Fatal(got, err)
	}
}

func TestServerProxy(t *testing.T) {
	ctx := context.Background()
	dir, err := os.MkdirTemp("/tmp", "darsena-proxy-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	service, err := Open(ctx, filepath.Join(dir, "db"), filepath.Join(dir, "sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	vault, err := configvault.Open(ctx, filepath.Join(dir, "config.db"), identity)
	if err != nil {
		t.Fatal(err)
	}
	if err = service.EnableConfig(vault); err != nil {
		t.Fatal(err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- service.Run(runCtx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	server := httptest.NewServer(transport.API{Store: demo.New(time.Second), WorkerSocket: filepath.Join(dir, "sock")}.Handler(""))
	defer server.Close()
	resp, err := http.Post(server.URL+"/api/v1/worker/jobs", "application/json", bytes.NewBufferString(`{"resource_id":"app","idempotency_key":"proxy-key"}`))
	if err != nil {
		t.Fatal(err)
	}
	var job Response
	if err = json.NewDecoder(resp.Body).Decode(&job); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 202 || job.ID == "" {
		t.Fatal(resp.StatusCode, job)
	}
	resp, err = http.Get(server.URL + "/api/v1/worker/jobs/" + job.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	image := "ghcr.io/example/app@sha256:" + strings.Repeat("a", 64)
	created, err := http.Post(server.URL+"/api/v1/worker/applications", "application/json", strings.NewReader(`{"name":"proxy-app","image":"`+image+`","port":8080}`))
	if err != nil {
		t.Fatal(err)
	}
	var app jobs.Application
	if err = json.NewDecoder(created.Body).Decode(&app); err != nil {
		t.Fatal(err)
	}
	created.Body.Close()
	if created.StatusCode != 201 {
		t.Fatal(created.StatusCode)
	}
	req, err := http.NewRequest("PUT", server.URL+"/api/v1/worker/config/"+app.ID+"/variables/TOKEN", strings.NewReader(`{"revision":0,"value":"proxy-secret","secret":true}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	saved, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer saved.Body.Close()
	data, err := io.ReadAll(saved.Body)
	if err != nil || saved.StatusCode != 200 || strings.Contains(string(data), "proxy-secret") {
		t.Fatal(saved.StatusCode, string(data), err)
	}
	projectResp, err := http.Post(server.URL+"/api/v1/worker/projects", "application/json", strings.NewReader(`{"name":"Proxy","color":"green"}`))
	if err != nil {
		t.Fatal(err)
	}
	projectBody, _ := io.ReadAll(projectResp.Body)
	projectResp.Body.Close()
	if projectResp.StatusCode != 201 || !strings.Contains(string(projectBody), `"name":"Proxy"`) {
		t.Fatal(projectResp.StatusCode, string(projectBody))
	}
	listResp, err := http.Get(server.URL + "/api/v1/worker/projects")
	if err != nil {
		t.Fatal(err)
	}
	listBody, _ := io.ReadAll(listResp.Body)
	listResp.Body.Close()
	if listResp.StatusCode != 200 || !strings.Contains(string(listBody), `"name":"Proxy"`) {
		t.Fatal(listResp.StatusCode, string(listBody))
	}

}

func TestEncryptedConfigurationAPI(t *testing.T) {
	ctx := context.Background()
	dir, err := os.MkdirTemp("/tmp", "darsena-config-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	service, err := Open(ctx, filepath.Join(dir, "jobs.db"), filepath.Join(dir, "sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	vault, err := configvault.Open(ctx, filepath.Join(dir, "config.db"), id)
	if err != nil {
		t.Fatal(err)
	}
	if err = service.EnableConfig(vault); err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		service.mux.ServeHTTP(w, r)
		return w
	}
	image := "ghcr.io/example/app@sha256:" + strings.Repeat("a", 64)
	created := request("POST", "/applications", `{"name":"example","image":"`+image+`","port":8080}`)
	if created.Code != 201 {
		t.Fatal(created.Code, created.Body.String())
	}
	var app jobs.Application
	if err = json.Unmarshal(created.Body.Bytes(), &app); err != nil {
		t.Fatal(err)
	}
	prefix := "/config/" + app.ID
	w := request("PUT", prefix+"/variables/PASSWORD", `{"revision":0,"value":"very-secret","secret":true}`)
	if w.Code != 200 || strings.Contains(w.Body.String(), "very-secret") {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request("GET", prefix, "")
	if w.Code != 200 || strings.Contains(w.Body.String(), "very-secret") || !strings.Contains(w.Body.String(), `"revision":1`) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request("PUT", prefix+"/variables/PASSWORD", `{"revision":0,"value":"replacement","secret":true}`)
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request("PUT", prefix+"/variables/PORT", `{"revision":1,"value":"8080","secret":false}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "8080") {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request("GET", prefix+"/history", "")
	if w.Code != 200 || !strings.HasPrefix(w.Body.String(), `{"entries":[{"revision":2,`) || !strings.Contains(w.Body.String(), `"revision":1,`) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request("DELETE", prefix+"/variables/PASSWORD", `{"revision":2}`)
	if w.Code != 200 || strings.Contains(w.Body.String(), "PASSWORD") {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestOpenRejectsUnsafeLock(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	db, sock := filepath.Join(dir, "state"), filepath.Join(dir, "socket")
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, db+".lock"); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(context.Background(), db, sock); err == nil {
		s.Close()
		t.Fatal("symlink lock accepted")
	}
	if err := os.Remove(db + ".lock"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(db+".lock", nil, 0644); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(context.Background(), db, sock); err == nil {
		s.Close()
		t.Fatal("public lock accepted")
	}
	if _, err := os.Stat(db); !os.IsNotExist(err) {
		t.Fatal("state created before lock rejection")
	}
}

type apiHarness struct {
	t       *testing.T
	service *Service
	vault   *configvault.Vault
	dir     string
}

func newAPIHarness(t *testing.T) *apiHarness {
	t.Helper()
	ctx := context.Background()
	dir, err := os.MkdirTemp("/tmp", "darsena-api-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	service, err := Open(ctx, filepath.Join(dir, "jobs.db"), filepath.Join(dir, "sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	vault, err := configvault.Open(ctx, filepath.Join(dir, "config.db"), id)
	if err != nil {
		t.Fatal(err)
	}
	if err = service.EnableConfig(vault); err != nil {
		t.Fatal(err)
	}
	return &apiHarness{t: t, service: service, vault: vault, dir: dir}
}

func (h *apiHarness) request(method, path, body string) *httptest.ResponseRecorder {
	h.t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.service.mux.ServeHTTP(w, r)
	if strings.Contains(w.Body.String(), "very-secret") {
		h.t.Fatal("secret disclosed", w.Body.String())
	}
	return w
}

func (h *apiHarness) expect(w *httptest.ResponseRecorder, status int, code string) {
	h.t.Helper()
	if w.Code != status || (code != "" && !strings.Contains(w.Body.String(), `"code":"`+code+`"`)) {
		h.t.Fatalf("want %d %s, got %d %s", status, code, w.Code, w.Body.String())
	}
}

func (h *apiHarness) application(name string) jobs.Application {
	h.t.Helper()
	image := "ghcr.io/example/app@sha256:" + strings.Repeat("a", 64)
	w := h.request("POST", "/applications", `{"name":"`+name+`","image":"`+image+`","port":8080}`)
	h.expect(w, 201, "")
	var app jobs.Application
	if err := json.Unmarshal(w.Body.Bytes(), &app); err != nil {
		h.t.Fatal(err)
	}
	return app
}

func TestProjectsAPI(t *testing.T) {
	h := newAPIHarness(t)
	a, b := h.application("alpha"), h.application("bravo")
	w := h.request("POST", "/projects", `{"name":"Web","color":"blue"}`)
	h.expect(w, 201, "")
	var p jobs.Project
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil || p.Version != 1 {
		t.Fatal(err, w.Body.String())
	}
	h.expect(h.request("POST", "/projects", `{"name":" web ","color":"red"}`), 409, "project_name_taken")
	h.expect(h.request("POST", "/projects", `{"name":"","color":"red"}`), 400, "invalid_project")
	h.expect(h.request("POST", "/projects", `{"name":"x","color":"puce"}`), 400, "invalid_project")
	if w = h.request("GET", "/projects", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"name":"Web"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = h.request("PATCH", "/projects/"+p.ID, `{"version":1,"name":"Frontend","color":"teal"}`)
	h.expect(w, 200, "")
	h.expect(h.request("PATCH", "/projects/"+p.ID, `{"version":1,"name":"Again","color":"teal"}`), 409, "update_conflict")
	other := h.request("POST", "/projects", `{"name":"Other","color":"red"}`)
	h.expect(other, 201, "")
	h.expect(h.request("PATCH", "/projects/"+p.ID, `{"version":2,"name":"other","color":"teal"}`), 409, "project_name_taken")
	h.expect(h.request("PATCH", "/projects/nothing", `{"version":1,"name":"Z","color":"teal"}`), 404, "project_not_found")

	w = h.request("POST", "/applications/assign", `{"project_id":"`+p.ID+`","application_ids":["`+a.ID+`","`+b.ID+`"]}`)
	h.expect(w, 200, "")
	if strings.Count(w.Body.String(), `"project_id":"`+p.ID+`"`) != 2 {
		t.Fatal(w.Body.String())
	}
	h.expect(h.request("POST", "/applications/assign", `{"project_id":"gone","application_ids":["`+a.ID+`"]}`), 404, "project_not_found")
	h.expect(h.request("POST", "/applications/assign", `{"project_id":"`+p.ID+`","application_ids":["missing"]}`), 404, "application_not_found")

	w = h.request("DELETE", "/projects/"+p.ID, "")
	h.expect(w, 200, "")
	var deleted struct {
		Project        jobs.Project `json:"project"`
		ApplicationIDs []string     `json:"application_ids"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &deleted); err != nil || deleted.Project.ID != p.ID || len(deleted.ApplicationIDs) != 2 {
		t.Fatal(err, w.Body.String())
	}
	h.expect(h.request("DELETE", "/projects/"+p.ID, ""), 404, "project_not_found")
	// Undo: the deleted project cannot receive applications.
	h.expect(h.request("POST", "/applications/assign", `{"project_id":"`+p.ID+`","application_ids":["`+a.ID+`"],"only_unassigned":true}`), 404, "project_not_found")
	if w = h.request("GET", "/applications/"+a.ID, ""); !strings.Contains(w.Body.String(), `"project_id":null`) {
		t.Fatal(w.Body.String())
	}
}

func TestApplicationEditAndDelete(t *testing.T) {
	ctx := context.Background()
	h := newAPIHarness(t)
	app := h.application("example")
	newImage := "ghcr.io/example/app@sha256:" + strings.Repeat("b", 64)
	h.expect(h.request("PUT", "/config/"+app.ID+"/variables/PASSWORD", `{"revision":0,"value":"very-secret","secret":true}`), 200, "")

	// Without any runtime the image and the port are editable.
	w := h.request("PATCH", "/applications/"+app.ID, `{"version":1,"image":"`+newImage+`","port":9090}`)
	h.expect(w, 200, "")
	h.expect(h.request("PATCH", "/applications/"+app.ID, `{"version":1,"image":"`+newImage+`","port":9091}`), 409, "update_conflict")
	h.expect(h.request("PATCH", "/applications/"+app.ID, `{"version":2,"image":"bad","port":9091}`), 400, "invalid_image")
	h.expect(h.request("PATCH", "/applications/"+app.ID, `{"version":2,"image":"nginx:1.27","port":9091}`), 400, "invalid_image")
	h.expect(h.request("PATCH", "/applications/missing", `{"version":1,"image":"`+newImage+`","port":9091}`), 404, "application_not_found")

	// A guard path that exists makes the unknown runtime state fail closed.
	guard := filepath.Join(h.dir, "intent.db")
	h.service.GuardIntentDB(guard)
	if err := os.WriteFile(guard, nil, 0600); err != nil {
		t.Fatal(err)
	}
	h.expect(h.request("PATCH", "/applications/"+app.ID, `{"version":2,"image":"ghcr.io/example/app@sha256:`+strings.Repeat("c", 64)+`","port":9090}`), 409, "runtime_state_unknown")
	h.expect(h.request("PATCH", "/applications/"+app.ID, `{"version":2,"image":"`+newImage+`","port":9091}`), 200, "")
	h.expect(h.request("DELETE", "/applications/"+app.ID, ""), 409, "runtime_state_unknown")
	if err := os.Remove(guard); err != nil {
		t.Fatal(err)
	}

	h.expect(h.request("DELETE", "/applications/"+app.ID, ""), 204, "")
	h.expect(h.request("GET", "/config/"+app.ID, ""), 404, "application_not_found")
	if rows, err := h.vault.History(ctx, app.ID); err != nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}
	if revs, err := h.vault.Revisions(ctx); err != nil || len(revs) != 0 {
		t.Fatal(revs, err)
	}
	h.expect(h.request("DELETE", "/applications/"+app.ID, ""), 404, "application_not_found")
	h.expect(h.request("PUT", "/config/"+app.ID+"/variables/X", `{"revision":0,"value":"v","secret":false}`), 404, "application_not_found")
}

func TestConfigHistoryAndRestoreAPI(t *testing.T) {
	h := newAPIHarness(t)
	app := h.application("example")
	prefix := "/config/" + app.ID
	noSecret := func(w *httptest.ResponseRecorder) {
		t.Helper()
		for _, secret := range []string{"first-secret", "second-secret"} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("response leaks a secret", w.Body.String())
			}
		}
	}

	// An existing application without configuration has an empty history.
	w := h.request("GET", prefix+"/history", "")
	h.expect(w, 200, "")
	if strings.TrimSpace(w.Body.String()) != `{"entries":[]}` {
		t.Fatal(w.Body.String())
	}
	h.expect(h.request("GET", "/config/missing/history", ""), 404, "application_not_found")
	h.expect(h.request("POST", prefix+"/variables/PASSWORD/restore", `{"revision":0,"from_revision":1}`), 409, "revision_not_retained")

	for _, body := range []string{
		`{"revision":0,"value":"first-secret","secret":true}`,
		`{"revision":1,"value":"second-secret","secret":true}`,
	} {
		w = h.request("PUT", prefix+"/variables/PASSWORD", body)
		h.expect(w, 200, "")
		noSecret(w)
	}
	w = h.request("GET", prefix+"/history", "")
	h.expect(w, 200, "")
	noSecret(w)
	var hist struct {
		Entries []struct {
			Revision int64 `json:"revision"`
			Changes  []struct {
				Name   string `json:"name"`
				Kind   string `json:"kind"`
				Secret bool   `json:"secret"`
			} `json:"changes"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &hist); err != nil {
		t.Fatal(err)
	}
	if len(hist.Entries) != 2 || hist.Entries[0].Revision != 2 || hist.Entries[1].Revision != 1 {
		t.Fatal(w.Body.String())
	}
	if c := hist.Entries[0].Changes; len(c) != 1 || c[0].Name != "PASSWORD" || c[0].Kind != "changed" || !c[0].Secret {
		t.Fatal(w.Body.String())
	}

	// Conflict, absent variable and invalid body.
	h.expect(h.request("POST", prefix+"/variables/PASSWORD/restore", `{"revision":1,"from_revision":1}`), 409, "configuration_conflict")
	h.expect(h.request("POST", prefix+"/variables/OTHER/restore", `{"revision":2,"from_revision":1}`), 409, "variable_not_in_revision")
	h.expect(h.request("POST", prefix+"/variables/PASSWORD/restore", `{"revision":2}`), 400, "invalid_configuration")
	h.expect(h.request("POST", prefix+"/variables/PASSWORD/restore", `{"revision":2,"from_revision":0}`), 400, "invalid_configuration")
	h.expect(h.request("POST", prefix+"/variables/PASSWORD/restore", `{"revision":2,"from_revision":1,"x":1}`), 400, "")

	w = h.request("POST", prefix+"/variables/PASSWORD/restore", `{"revision":2,"from_revision":1}`)
	h.expect(w, 200, "")
	noSecret(w)
	if !strings.Contains(w.Body.String(), `"revision":3`) || !strings.Contains(w.Body.String(), `"PASSWORD"`) {
		t.Fatal(w.Body.String())
	}
	w = h.request("GET", prefix, "")
	h.expect(w, 200, "")
	noSecret(w)
	if !strings.Contains(w.Body.String(), `"revision":3`) {
		t.Fatal(w.Body.String())
	}
	w = h.request("GET", prefix+"/history", "")
	noSecret(w)
	if !strings.HasPrefix(w.Body.String(), `{"entries":[{"revision":3,`) {
		t.Fatal(w.Body.String())
	}

	// Revisions beyond the retention limit are pruned.
	for rev := int64(3); rev < 30; rev++ {
		h.expect(h.request("PUT", prefix+"/variables/COUNTER", fmt.Sprintf(`{"revision":%d,"value":"%d","secret":false}`, rev, rev)), 200, "")
	}
	h.expect(h.request("POST", prefix+"/variables/PASSWORD/restore", `{"revision":30,"from_revision":1}`), 409, "revision_not_retained")

	// A deleted application has no history or restore.
	h.expect(h.request("DELETE", "/applications/"+app.ID, ""), 204, "")
	h.expect(h.request("GET", prefix+"/history", ""), 404, "application_not_found")
	h.expect(h.request("POST", prefix+"/variables/PASSWORD/restore", `{"revision":30,"from_revision":29}`), 404, "application_not_found")
}

func TestDeleteDoesNotLeaveConfigurationUnderConcurrentWrites(t *testing.T) {
	ctx := context.Background()
	for round := 0; round < 20; round++ {
		h := newAPIHarness(t)
		app := h.application("racing")
		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < 10; i++ {
				w := h.request("PUT", "/config/"+app.ID+"/variables/V"+strconv.Itoa(i), `{"revision":`+strconv.Itoa(i)+`,"value":"very-secret","secret":true}`)
				if w.Code != 200 && w.Code != 404 && w.Code != 409 {
					t.Error(w.Code, w.Body.String())
				}
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if w := h.request("DELETE", "/applications/"+app.ID, ""); w.Code != 204 {
				t.Error(w.Code, w.Body.String())
			}
		}()
		close(start)
		wg.Wait()
		if revs, err := h.vault.Revisions(ctx); err != nil || len(revs) != 0 {
			t.Fatal("orphan configuration", revs, err)
		}
		if rows, err := h.vault.History(ctx, app.ID); err != nil || len(rows) != 0 {
			t.Fatal("orphan history", rows, err)
		}
	}
}

func TestApplicationImagesAreStoredPinnedAndCanonical(t *testing.T) {
	h := newAPIHarness(t)
	digest := "sha256:" + strings.Repeat("a", 64)
	w := h.request("POST", "/applications", `{"name":"hola","image":"hello-world:nanoserver@sha256:`+strings.Repeat("A", 64)+`","port":8080}`)
	h.expect(w, 201, "")
	var app jobs.Application
	if err := json.Unmarshal(w.Body.Bytes(), &app); err != nil || app.Image != "docker.io/library/hello-world:nanoserver@"+digest {
		t.Fatalf("stored image %q (%v)", app.Image, err)
	}
	h.expect(h.request("POST", "/applications", `{"name":"sin-digest","image":"nginx:1.27","port":8080}`), 400, "invalid_image")
}
