package transport

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHealthTracksWorkerAvailability(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "darsena-ready-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "sock")
	check := func(h http.Handler, path string, status int, body string) {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "http://localhost"+path, nil))
		if w.Code != status || !strings.Contains(w.Body.String(), body) {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	check(API{}.Handler(""), "/api/v1/health/ready", 200, `"worker":"disabled"`)
	h := API{WorkerSocket: socket}.Handler("")
	check(h, "/api/v1/health/live", 200, "alive")
	check(h, "/api/v1/health/ready", 503, `"worker":"unavailable"`)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })}
	go srv.Serve(ln)
	check(h, "/api/v1/health/ready", 200, `"worker":"ready"`)
	srv.Close()
	check(h, "/api/v1/health/ready", 503, `"worker":"unavailable"`)
	check(h, "/api/v1/health/live", 200, "alive")
}

func TestReadinessTimeout(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "darsena-timeout-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan struct{})
	go func() {
		c, e := ln.Accept()
		if e == nil {
			defer c.Close()
			<-done
		}
	}()
	defer close(done)
	h := API{WorkerSocket: socket}.Handler("")
	w := httptest.NewRecorder()
	start := time.Now()
	h.ServeHTTP(w, httptest.NewRequest("GET", "http://localhost/api/v1/health/ready", nil))
	if w.Code != 503 || time.Since(start) > 2*time.Second {
		t.Fatalf("status %d duration %v", w.Code, time.Since(start))
	}
}
