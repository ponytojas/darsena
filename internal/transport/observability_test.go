package transport

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRequestLogAndProxyCorrelation(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(old)
	dir, err := os.MkdirTemp("/tmp", "darsena-proxy-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	const workerID = "0123456789abcdef0123456789abcdef"
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", workerID)
		w.WriteHeader(202)
	})}
	go srv.Serve(ln)
	defer srv.Close()
	h := API{WorkerSocket: socket}.Handler("")
	req := httptest.NewRequest("GET", "http://localhost/api/v1/worker/jobs/secret-path?token=secret-query", nil)
	req.Header.Set("X-Request-ID", "secret-header")
	out := httptest.NewRecorder()
	h.ServeHTTP(out, req)
	id := out.Header().Get("X-Request-ID")
	if out.Code != 202 || len(id) != 32 || id == workerID {
		t.Fatalf("status %d id %q", out.Code, id)
	}
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &record); err != nil {
		t.Fatal(err)
	}
	if record["request_id"] != id || record["worker_request_id"] != workerID || record["route"] != "/api/v1/worker/" || record["status"] != float64(202) {
		t.Fatalf("record: %#v", record)
	}
	for _, secret := range []string{"secret-path", "secret-query", "secret-header"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatal("sensitive request data was logged")
		}
	}
}

func TestRequestLogForRejectedHost(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(old)
	h := API{}.Handler("")
	out := httptest.NewRecorder()
	h.ServeHTTP(out, httptest.NewRequest("GET", "http://secret-host/path", nil))
	if out.Code != 403 || strings.Contains(logs.String(), "secret-host") {
		t.Fatalf("status %d logs %s", out.Code, logs.String())
	}
}
