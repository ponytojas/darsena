package worker

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/ponytojas/darsena/internal/platform/configvault"
	"github.com/ponytojas/darsena/internal/platform/jobs"
)

func TestReadinessChecksDependencies(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "darsena-health-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	s, err := Open(context.Background(), filepath.Join(dir, "state"), filepath.Join(dir, "sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	check := func(path string, code int, body string) {
		t.Helper()
		w := httptest.NewRecorder()
		s.mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != code || !strings.Contains(w.Body.String(), body) {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	check("/livez", 200, "alive")
	check("/readyz", 200, `"vault":"disabled"`)
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	v, err := configvault.Open(context.Background(), filepath.Join(dir, "vault"), identity)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnableConfig(v); err != nil {
		t.Fatal(err)
	}
	check("/readyz", 200, `"vault":"ready"`)
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	check("/readyz", 503, `"vault":"unavailable"`)
	if err := s.store.Close(); err != nil {
		t.Fatal(err)
	}
	check("/readyz", 503, `"state":"unavailable"`)
	check("/livez", 200, "alive")
}

func TestJobLogsCorrelateWithoutRequestData(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(old)
	dir, err := os.MkdirTemp("/tmp", "darsena-log-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "sock")
	s, err := Open(context.Background(), filepath.Join(dir, "state"), socket)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}
	resp, err := client.Post("http://worker/jobs", "application/json", strings.NewReader(`{"resource_id":"secret-marker","idempotency_key":"secret-key"}`))
	if err != nil {
		t.Fatal(err)
	}
	var job Response
	if err = json.NewDecoder(resp.Body).Decode(&job); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("X-Request-ID") == "" {
		t.Fatal("missing request ID")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		r, e := client.Get("http://worker/jobs/" + job.ID)
		if e != nil {
			t.Fatal(e)
		}
		var j Response
		e = json.NewDecoder(r.Body).Decode(&j)
		r.Body.Close()
		if e != nil {
			t.Fatal(e)
		}
		if j.Status == "succeeded" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(&logs)
	if err != nil {
		t.Fatal(err)
	}
	output := string(data)
	for _, part := range []string{"job_enqueued", "job_claimed", "job_completed", job.ID, "request_id"} {
		if !strings.Contains(output, part) {
			t.Fatalf("missing %s in %s", part, output)
		}
	}
	if strings.Contains(output, "secret-marker") || strings.Contains(output, "secret-key") {
		t.Fatal("request data leaked")
	}
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		var v map[string]any
		if json.Unmarshal([]byte(line), &v) != nil {
			t.Fatalf("invalid JSON log: %s", line)
		}
	}
}

func TestRecoveryLogsEachSafeIDAcrossBatches(t *testing.T) {
	ctx := context.Background()
	dir, err := os.MkdirTemp("/tmp", "darsena-recovery-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path, socket := filepath.Join(dir, "state"), filepath.Join(dir, "sock")
	store, err := jobs.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i := 0; i < 102; i++ {
		j, err := store.Enqueue(ctx, fmt.Sprintf("resource-%d-secret", i), "simulation", fmt.Sprintf("key-%d-secret", i))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.ClaimNext(ctx); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, j.ID)
	}
	queued, err := store.Enqueue(ctx, "queued", "simulation", "queued-key")
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := store.Enqueue(ctx, "terminal", "simulation", "terminal-key")
	if err != nil {
		t.Fatal(err)
	}
	// Claim the queued job, finish it, then leave the other job queued.
	claimed, err := store.ClaimNext(ctx)
	if err != nil || claimed.ID != queued.ID {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	if _, err = store.Complete(ctx, queued.ID, "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	queued, terminal = terminal, queued
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE jobs SET id=? WHERE id=?", "secret-malformed-id", ids[0]); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(old)
	s, err := Open(ctx, path, socket)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids[1:] {
		j, err := s.store.Get(ctx, id)
		if err != nil || j.Status != "interrupted" {
			t.Fatalf("recovery %s: %v %v", id, j, err)
		}
		if !strings.Contains(logs.String(), id) {
			t.Fatalf("missing event for %s", id)
		}
	}
	j, err := s.store.Get(ctx, queued.ID)
	if err != nil || j.Status != "queued" {
		t.Fatalf("queued changed: %v %v", j, err)
	}
	j, err = s.store.Get(ctx, terminal.ID)
	if err != nil || j.Status != "succeeded" {
		t.Fatalf("terminal changed: %v %v", j, err)
	}
	if strings.Count(logs.String(), `"msg":"job_interrupted"`) != 102 {
		t.Fatalf("incorrect recovery events")
	}
	if strings.Contains(logs.String(), "secret") {
		t.Fatal("persisted data leaked in logs")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	logs.Reset()
	s, err = Open(ctx, path, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if strings.Contains(logs.String(), "job_interrupted") {
		t.Fatal("duplicate recovery event")
	}
}

func TestCompletionFailureIsSafeAndStopsHTTP(t *testing.T) {
	ctx := context.Background()
	dir, err := os.MkdirTemp("/tmp", "darsena-failure-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path, socket := filepath.Join(dir, "state"), filepath.Join(dir, "sock")
	s, err := Open(ctx, path, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	j, err := s.store.Enqueue(ctx, "private-resource", "simulation", "private-key")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TRIGGER fail_completion BEFORE UPDATE OF status ON jobs WHEN NEW.status='succeeded' BEGIN SELECT RAISE(FAIL, 'private-sql-error'); END`)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(old)
	runCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	err = s.Run(runCtx)
	if err == nil || err.Error() != "unable to complete job" {
		t.Fatalf("unexpected failure: %v", err)
	}
	if !strings.Contains(logs.String(), "job_completion_failed") || !strings.Contains(logs.String(), j.ID) {
		t.Fatal("missing failure context")
	}
	if strings.Contains(logs.String(), "private-") {
		t.Fatal("sensitive data in logs")
	}
	if _, err := net.DialTimeout("unix", socket, 100*time.Millisecond); err == nil {
		t.Fatal("HTTP listener still open")
	}
	got, err := s.store.Get(ctx, j.ID)
	if err != nil || got.Status != "running" {
		t.Fatalf("job should remain recoverable: %v %v", got, err)
	}
}
