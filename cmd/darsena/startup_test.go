package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/ponytojas/darsena/internal/platform/jobs"
)

func TestWorkerStartupPersistenceAndWrongIdentity(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "darsena-start-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	identity := filepath.Join(dir, "identity")
	if err = os.WriteFile(identity, []byte(id.String()), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"worker", "--demo", "--state", filepath.Join(dir, "state"), "--socket", filepath.Join(dir, "socket"), "--config-db", filepath.Join(dir, "config"), "--age-identity", identity}
	start := func() (context.CancelFunc, chan error) {
		t.Helper()
		cfg, err := resolveConfig(args, env(nil))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		service, err := startWorker(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { err := service.Run(ctx); _ = service.Close(); done <- err }()
		return cancel, done
	}
	stop := func(cancel context.CancelFunc, done chan error) {
		t.Helper()
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("worker did not stop")
		}
	}
	client := http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", args[5])
	}}}
	request := func(method, path, body string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(method, "http://worker"+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, string(data)
	}
	cancel, done := start()
	status, _ := request("GET", "/health", "")
	if status != 200 {
		t.Fatal(status)
	}
	status, body := request("POST", "/applications", `{"name":"example","image":"ghcr.io/example/app@sha256:`+strings.Repeat("a", 64)+`","port":8080}`)
	if status != 201 {
		t.Fatal(status, body)
	}
	var app struct {
		ID string `json:"id"`
	}
	if err = json.Unmarshal([]byte(body), &app); err != nil || app.ID == "" {
		t.Fatal(body, err)
	}
	status, body = request("PUT", "/config/"+app.ID+"/variables/PASSWORD", `{"revision":0,"value":"SECRET_MARKER","secret":true}`)
	if status != 200 || strings.Contains(body, "SECRET_MARKER") {
		t.Fatal(status, body)
	}
	stop(cancel, done)
	client.CloseIdleConnections()
	cancel, done = start()
	status, body = request("GET", "/config/"+app.ID, "")
	if status != 200 || !strings.Contains(body, `"revision":1`) || strings.Contains(body, "SECRET_MARKER") {
		t.Fatal(status, body)
	}
	stop(cancel, done)
	client.CloseIdleConnections()
	store, err := jobs.Open(context.Background(), args[3])
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.Enqueue(context.Background(), "running-app", "simulation", "running-request")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ClaimNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	other, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(identity, []byte(other.String()), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := resolveConfig(args, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	_, err = startWorker(context.Background(), cfg)
	if err == nil || strings.Contains(err.Error(), other.String()) {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err = os.Lstat(args[5]); !os.IsNotExist(err) {
		t.Fatal("socket created with wrong identity")
	}
	store, err = jobs.Open(context.Background(), args[3])
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, err := store.Get(context.Background(), job.ID)
	if err != nil || got.Status != "running" {
		t.Fatalf("job changed on failed startup: %+v %v", got, err)
	}

}
