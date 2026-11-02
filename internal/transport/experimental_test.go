package transport

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ponytojas/darsena/internal/platform/ownerauth"
)

func TestExperimentalProxyRequiresAuthenticationAndMatchingWorker(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(home, ".darsena-auth-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	credential := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{42}, 32))
	path := filepath.Join(dir, "credential")
	if err = os.WriteFile(path, []byte(credential+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	owner, err := ownerauth.New(path, "http://127.0.0.1:7300")
	if err != nil {
		t.Fatal(err)
	}
	sockDir, err := os.MkdirTemp("/tmp", "darsena-experimental-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sockDir)
	sock := filepath.Join(sockDir, "worker.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var matching atomic.Bool
	matching.Store(true)
	var providerReady atomic.Bool
	providerReady.Store(true)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" {
			_, _ = io.WriteString(w, `{"mode":"experimental","state":"ready","vault":"ready"}`)
			return
		}
		if r.URL.Path == "/runtime/ready" {
			if !providerReady.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, `{"status":"unavailable"}`)
			} else {
				_, _ = io.WriteString(w, `{"status":"ready"}`)
			}
			return
		}
		if r.URL.Path == "/runtime/info" {
			if matching.Load() {
				_, _ = io.WriteString(w, `{"mode":"experimental","installation":"install","policy":"dockerlocal-v3"}`)
			} else {
				_, _ = io.WriteString(w, `{"mode":"experimental","installation":"other","policy":"dockerlocal-v3"}`)
			}
			return
		}
		calls.Add(1)
		if r.Header.Get("Cookie") != "" || r.Header.Get("X-Darsena-CSRF") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Origin") != "" {
			t.Error("credential forwarded to worker")
		}
		w.Header().Set("Set-Cookie", "evil=1")
		w.WriteHeader(http.StatusNoContent)
	})}
	go server.Serve(ln)
	defer server.Close()
	h := API{Owner: owner, Installation: "install", WorkerSocket: sock}.Handler("")
	request := func(method, path, origin, csrf string, c *http.Cookie, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1:7300"+path, strings.NewReader(body))
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if csrf != "" {
			r.Header.Set("X-Darsena-CSRF", csrf)
		}
		if c != nil {
			r.AddCookie(c)
		}
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	target := "/api/v1/worker/applications/example/service"
	caps := request("GET", "/api/v1/capabilities", "", "", nil, "")
	if !strings.Contains(caps.Body.String(), `"provider":"ready"`) {
		t.Fatal(caps.Body.String())
	}
	providerReady.Store(false)
	caps = request("GET", "/api/v1/capabilities", "", "", nil, "")
	if !strings.Contains(caps.Body.String(), `"provider":"unavailable"`) {
		t.Fatal(caps.Body.String())
	}
	providerReady.Store(true)
	if w := request("POST", target, "http://127.0.0.1:7300", "", nil, `{}`); w.Code != 401 {
		t.Fatal(w.Code)
	}
	login := request("POST", "/api/v1/auth/login", "http://127.0.0.1:7300", "", nil, `{"credential":"`+credential+`"}`)
	if login.Code != 200 {
		t.Fatal(login.Code)
	}
	var payload struct {
		CSRF string `json:"csrf"`
	}
	if json.Unmarshal(login.Body.Bytes(), &payload) != nil {
		t.Fatal("bad login")
	}
	cookie := login.Result().Cookies()[0]
	if w := request("POST", target, "http://127.0.0.1:7300", "bad", cookie, `{}`); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := request("GET", target, "", "", cookie, ""); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := request("POST", target, "http://127.0.0.1:9999", payload.CSRF, cookie, `{}`); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if calls.Load() != 0 {
		t.Fatal("unauthorized request reached worker")
	}
	for _, action := range []string{"stop", "remove"} {
		if w := request("POST", target+"/"+action, "http://127.0.0.1:7300", "bad", cookie, `{}`); w.Code != 403 {
			t.Fatalf("unauthorized %s: %d", action, w.Code)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("unauthorized lifecycle action reached worker")
	}
	providerReady.Store(false)
	if w := request("POST", target, "http://127.0.0.1:7300", payload.CSRF, cookie, `{}`); w.Code != 503 {
		t.Fatal(w.Code)
	}
	if calls.Load() != 0 {
		t.Fatal("unavailable runtime reached")
	}
	providerReady.Store(true)
	matching.Store(false)
	if w := request("POST", target, "http://127.0.0.1:7300", payload.CSRF, cookie, `{}`); w.Code != 503 {
		t.Fatal(w.Code)
	}
	if calls.Load() != 0 {
		t.Fatal("mismatched worker reached")
	}
	matching.Store(true)
	w := request("POST", target, "http://127.0.0.1:7300", payload.CSRF, cookie, `{}`)
	if w.Code != 204 || w.Header().Get("Set-Cookie") != "" || calls.Load() != 1 {
		t.Fatalf("%d %d", w.Code, calls.Load())
	}
	for _, action := range []string{"stop", "remove"} {
		w = request("POST", target+"/"+action, "http://127.0.0.1:7300", payload.CSRF, cookie, `{}`)
		if w.Code != 204 {
			t.Fatalf("authorized %s: %d", action, w.Code)
		}
	}
	demo := API{WorkerSocket: sock}.Handler("")
	wr := httptest.NewRecorder()
	demo.ServeHTTP(wr, httptest.NewRequest("POST", "http://127.0.0.1:7300"+target, nil))
	if wr.Code != 403 || calls.Load() != 3 {
		t.Fatal("demo forwarded real operation")
	}
}
