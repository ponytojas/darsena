package ownerauth

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testOrigin = "http://127.0.0.1:8080"

func testManager(t *testing.T) (*Manager, string, string) {
	t.Helper()
	credential := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{42}, 32))
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "credential")
	if err := os.WriteFile(path, []byte(credential+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m, err := New(path, testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	return m, credential, path
}

func login(t *testing.T, m *Manager, credential string) (*http.Cookie, string) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, testOrigin+"/login", strings.NewReader(`{"credential":"`+credential+`"}`))
	r.Header.Set("Origin", testOrigin)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	m.Login(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("login status: %d", w.Code)
	}
	var response struct {
		CSRF string `json:"csrf"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing session cookie")
	}
	return cookies[0], response.CSRF
}

func TestSessionLifecycleAndCSRF(t *testing.T) {
	m, credential, _ := testManager(t)
	cookie, csrf := login(t, m, credential)
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.MaxAge <= 0 {
		t.Fatal("unsafe cookie")
	}
	var calls atomic.Int32
	h := m.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(http.StatusNoContent) }))
	request := func(method, host, origin, token string, c *http.Cookie) int {
		r := httptest.NewRequest(method, host+"/protected", nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if token != "" {
			r.Header.Set(csrfHeader, token)
		}
		if c != nil {
			r.AddCookie(c)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if got := request("POST", testOrigin, testOrigin, csrf, nil); got != 401 {
		t.Fatal(got)
	}
	if got := request("POST", testOrigin, "http://evil.example", csrf, cookie); got != 403 {
		t.Fatal(got)
	}
	if got := request("POST", testOrigin, testOrigin, "wrong", cookie); got != 403 {
		t.Fatal(got)
	}
	if got := request("POST", "http://localhost:8080", testOrigin, csrf, cookie); got != 403 {
		t.Fatal(got)
	}
	if got := request("POST", testOrigin, testOrigin, csrf, cookie); got != 204 {
		t.Fatal(got)
	}
	// Cookies are shared by hostname across ports; a stolen cookie alone must
	// not grant read access on the protected origin.
	if got := request("GET", testOrigin, "", "", cookie); got != 403 {
		t.Fatal(got)
	}
	if got := request("GET", testOrigin, "http://127.0.0.1:9999", csrf, cookie); got != 403 {
		t.Fatal(got)
	}
	if got := request("GET", testOrigin, "", csrf, cookie); got != 204 {
		t.Fatal(got)
	}
	if calls.Load() != 2 {
		t.Fatal("unauthorized handler invocation")
	}
	r := httptest.NewRequest("POST", testOrigin+"/logout", nil)
	r.Header.Set("Origin", testOrigin)
	r.Header.Set(csrfHeader, csrf)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	m.Require(http.HandlerFunc(m.Logout)).ServeHTTP(w, r)
	if w.Code != 204 || request("POST", testOrigin, testOrigin, csrf, cookie) != 401 {
		t.Fatal("logout did not revoke")
	}
}

func TestLoginRejectsInputAndRedacts(t *testing.T) {
	m, credential, _ := testManager(t)
	for _, tc := range []struct {
		body, origin, content string
		status                int
	}{
		{`{"credential":"` + credential + `"}`, "http://evil.example", "application/json", 403},
		{`{"credential":"wrong-secret-marker"}`, testOrigin, "application/json", 401},
		{`{"credential":"` + credential + `","extra":"wrong-secret-marker"}`, testOrigin, "application/json", 400},
		{`{"credential":"` + credential + `"}`, testOrigin, "text/plain", 415},
	} {
		r := httptest.NewRequest("POST", testOrigin+"/login", strings.NewReader(tc.body))
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Content-Type", tc.content)
		w := httptest.NewRecorder()
		m.Login(w, r)
		if w.Code != tc.status || strings.Contains(w.Body.String(), credential) || strings.Contains(w.Body.String(), "wrong-secret-marker") {
			t.Fatalf("status %d or redaction failure", w.Code)
		}
	}
}

func TestCredentialFileAndOriginValidation(t *testing.T) {
	_, _, path := testManager(t)
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := New(path, testOrigin); err == nil {
		t.Fatal("public credential accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(filepath.Dir(path), "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := New(link, testOrigin); err == nil {
		t.Fatal("symlink accepted")
	}
	for _, origin := range []string{"http://example.com:8080", "https://localhost:8080", "http://127.0.0.1:8080/path", "http://localhost", "http://localhost:99999"} {
		if _, err := New(path, origin); err == nil {
			t.Fatalf("origin accepted: %s", origin)
		}
	}
}

func TestGenerateDoesNotOverwrite(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "credential")
	if err := Generate(path); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(path, testOrigin); err != nil {
		t.Fatal(err)
	}
	if err := Generate(path); err == nil {
		t.Fatal("overwrote credential")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(first, after) {
		t.Fatal("credential changed")
	}
}

func TestRejectWritableAncestors(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	unsafe := filepath.Join(dir, "shared")
	if err := os.Mkdir(unsafe, 0700); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(unsafe, "private")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(child, "credential")
	if err := Generate(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unsafe, 0777); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(unsafe, 0700) })
	if _, err := New(path, testOrigin); err == nil {
		t.Fatal("accepted writable ancestor")
	}
	if err := Generate(filepath.Join(child, "second")); err == nil {
		t.Fatal("created under writable ancestor")
	}
}

func TestExpiryAndBoundedSessions(t *testing.T) {
	m, credential, _ := testManager(t)
	now := time.Now()
	m.now = func() time.Time { return now }
	cookie, csrf := login(t, m, credential)
	now = now.Add(sessionLifetime)
	r := httptest.NewRequest("POST", testOrigin+"/protected", nil)
	r.AddCookie(cookie)
	r.Header.Set("Origin", testOrigin)
	r.Header.Set(csrfHeader, csrf)
	w := httptest.NewRecorder()
	m.Require(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("expired session reached handler") })).ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	for i := 0; i < maxSessions; i++ {
		login(t, m, credential)
	}
	r = httptest.NewRequest("POST", testOrigin+"/login", strings.NewReader(`{"credential":"`+credential+`"}`))
	r.Header.Set("Origin", testOrigin)
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	m.Login(w, r)
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
}

func TestConcurrentSessions(t *testing.T) {
	m, credential, _ := testManager(t)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Go(func() { login(t, m, credential) })
	}
	wg.Wait()
}
