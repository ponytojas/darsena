package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"filippo.io/age"
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
	"strings"
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
	if w.Code != 200 || !strings.Contains(w.Body.String(), "[2,1]") {
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
