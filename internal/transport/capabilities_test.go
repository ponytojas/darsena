package transport

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCapabilitiesWorkerStates(t *testing.T) {
	cases := []struct {
		name                       string
		code                       int
		body, status, state, vault string
	}{
		{"ready", 200, `{"mode":"simulation","state":"ready","vault":"ready"}`, "ready", "ready", "ready"},
		{"no vault", 200, `{"mode":"simulation","state":"ready","vault":"disabled"}`, "ready", "ready", "disabled"},
		{"vault failed", 503, `{"mode":"simulation","state":"ready","vault":"unavailable"}`, "unavailable", "ready", "unavailable"},
		{"state failed", 503, `{"mode":"simulation","state":"unavailable","vault":"ready"}`, "unavailable", "unavailable", "ready"},
		{"inconsistent", 200, `{"mode":"simulation","state":"unavailable","vault":"ready"}`, "unavailable", "unknown", "unknown"},
		{"invalid", 200, `{"mode":"simulation","state":"secret-marker","vault":"ready"}`, "unavailable", "unknown", "unknown"},
		{"extra field", 200, `{"mode":"simulation","state":"ready","vault":"ready","secret":"secret-marker"}`, "unavailable", "unknown", "unknown"},
		{"oversize", 200, strings.Repeat("x", 4097), "unavailable", "unknown", "unknown"},
		{"redirect", 302, `secret-marker`, "unavailable", "unknown", "unknown"},
	}
	dir, err := os.MkdirTemp("/tmp", "darsena-cap-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var code int
	var body string
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" {
			t.Error("wrong path")
		}
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	})}
	go server.Serve(ln)
	defer server.Close()
	h := API{WorkerSocket: socket}.Handler("")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body = tc.code, tc.body
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", "http://localhost/api/v1/capabilities", nil))
			if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), "secret-marker") {
				t.Fatalf("unexpected response %d %s", w.Code, w.Body.String())
			}
			var got capabilitiesResponse
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Mode != "demo" || got.RealExecution.Enabled || got.RealExecution.Provider != "disabled" || got.RealExecution.Configuration != "not_applied" || !got.Worker.Configured || got.Worker.Status != tc.status || got.State != tc.state || got.Vault != tc.vault {
				t.Fatalf("unexpected response %+v", got)
			}
		})
	}
	server.Close()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "http://localhost/api/v1/capabilities", nil))
	var got capabilitiesResponse
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got.Worker.Status != "unavailable" || got.State != "unknown" || got.Vault != "unknown" {
		t.Fatalf("disconnected: %+v", got)
	}
}

func TestCapabilitiesDisabledAndTimeout(t *testing.T) {
	w := httptest.NewRecorder()
	API{}.Handler("").ServeHTTP(w, httptest.NewRequest("GET", "http://localhost/api/v1/capabilities", nil))
	var got capabilitiesResponse
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got.Worker.Configured || got.Worker.Status != "disabled" || got.State != "disabled" || got.Vault != "disabled" {
		t.Fatalf("disabled: %+v", got)
	}
	dir, err := os.MkdirTemp("/tmp", "darsena-cap-time-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			defer c.Close()
			<-done
		}
	}()
	start := time.Now()
	w = httptest.NewRecorder()
	API{WorkerSocket: socket}.Handler("").ServeHTTP(w, httptest.NewRequest("GET", "http://localhost/api/v1/capabilities", nil))
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if time.Since(start) > 3*time.Second || got.Worker.Status != "unavailable" || got.Vault != "unknown" {
		t.Fatalf("timeout: %+v", got)
	}
}
