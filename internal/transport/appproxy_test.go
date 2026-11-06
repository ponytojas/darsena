package transport

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
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

// workerFake answers GET /routes/{domain} on a unix socket.
type workerFake struct {
	mu     sync.Mutex
	routes map[string]string // domain -> body; "" body with status key below
	status map[string]int
	asked  atomic.Int32
	sock   string
}

func newWorkerFake(t *testing.T) *workerFake {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "darsena-apps-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	f := &workerFake{routes: map[string]string{}, status: map[string]int{}, sock: filepath.Join(dir, "w.sock")}
	ln, err := net.Listen("unix", f.sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.asked.Add(1)
		domain, ok := strings.CutPrefix(r.URL.Path, "/routes/")
		if !ok || r.Method != "GET" {
			t.Errorf("unexpected worker request %s %s", r.Method, r.URL.Path)
		}
		f.mu.Lock()
		body, known := f.routes[domain], f.status[domain]
		f.mu.Unlock()
		if known == 0 {
			known = 404
		}
		w.WriteHeader(known)
		_, _ = io.WriteString(w, body)
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return f
}

func (f *workerFake) set(domain string, status int, body string) {
	f.mu.Lock()
	f.routes[domain], f.status[domain] = body, status
	f.mu.Unlock()
}

func backendPort(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	_, p, _ := net.SplitHostPort(srv.Listener.Addr().String())
	var n int
	fmt.Sscan(p, &n)
	return n
}

func get(h http.Handler, host, path string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "http://"+host+path, nil)
	r.Host = host
	r.RemoteAddr = "127.0.0.1:50000"
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestAppDomainHosts(t *testing.T) {
	for host, want := range map[string]string{
		"mi-web.localhost":       "mi-web",
		"mi-web.localhost:7321":  "mi-web",
		"MI-Web.LOCALHOST":       "mi-web",
		"mi-web.localhost:80":    "mi-web",
		"mi-web.localhost:65535": "mi-web",
		"localhost":              "",
		"localhost:7321":         "",
		".localhost":             "",
		"a.b.localhost":          "",
		"mi-web.localhost.":      "",
		"mi-web.localhost:":      "",
		"mi-web.localhost:0":     "",
		"mi-web.localhost:65536": "",
		"mi-web.localhost:07321": "",
		"mi-web.localhost:abc":   "",
		"mi-web.example.com":     "",
		"mi-web.localhost.evil":  "",
		"evil-localhost":         "",
		"xmi-web.localhost.com":  "",
		"127.0.0.1":              "",
		"127.0.0.1:7321":         "",
		"[::1]:7321":             "",
		"www.localhost":          "",
		"api.localhost":          "",
		"a.localhost":            "",
		"1abc.localhost":         "",
		"abc-.localhost":         "",
		"mi_web.localhost":       "",
		"":                       "",
		"user@mi-web.localhost":  "",
		"mi-web.localhost/x":     "",
	} {
		got, ok := appDomain(host)
		if got != want || ok != (want != "") {
			t.Errorf("%q -> %q %v, want %q", host, got, ok, want)
		}
	}
}

func TestAppProxyRefusesBadHostsWithoutAskingTheWorker(t *testing.T) {
	f := newWorkerFake(t)
	p := &AppProxy{WorkerSocket: f.sock}
	for _, host := range []string{"localhost:7321", "a.b.localhost", "127.0.0.1:7321", "evil.example", "www.localhost"} {
		w := get(p, host, "/api/v1/status", nil)
		if w.Code != 404 || !strings.Contains(w.Body.String(), "Unknown address") {
			t.Errorf("%s: %d", host, w.Code)
		}
	}
	if f.asked.Load() != 0 {
		t.Fatal("worker asked about an invalid host")
	}
}

func TestAppProxyUnknownStoppedAndDown(t *testing.T) {
	f := newWorkerFake(t)
	p := &AppProxy{WorkerSocket: f.sock}
	if w := get(p, "nadie.localhost:7321", "/", nil); w.Code != 404 || !strings.Contains(w.Header().Get("Content-Type"), "text/html") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unknown: %d", w.Code)
	}
	f.set("parada", 503, `{"code":"not_running","error":"internal detail secret-marker"}`)
	w := get(p, "parada.localhost", "/", nil)
	if w.Code != 503 || strings.Contains(w.Body.String(), "secret-marker") || !strings.Contains(w.Body.String(), "not running") {
		t.Fatalf("stopped: %d %s", w.Code, w.Body)
	}
	// A bad answer is "down", never a port.
	for name, body := range map[string]string{"mala1": `{"host_port":0}`, "mala2": `{"host_port":70000}`, "mala3": `{"host_port":80,"x":1}`, "mala4": `nonsense`} {
		f.set(name, 200, body)
		if w := get(p, name+".localhost", "/", nil); w.Code != 503 {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
	// A listener nobody answers on: 502 with a plain page.
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	f.set("silenciosa", 200, fmt.Sprintf(`{"host_port":%d}`, port))
	w = get(p, "silenciosa.localhost", "/", nil)
	if w.Code != 502 || strings.Contains(w.Body.String(), "127.0.0.1") || strings.Contains(w.Body.String(), fmt.Sprint(port)) {
		t.Fatalf("down: %d %s", w.Code, w.Body)
	}
	// Worker gone entirely.
	gone := &AppProxy{WorkerSocket: filepath.Join(t.TempDir(), "none.sock")}
	if w := get(gone, "x1.localhost", "/", nil); w.Code != 503 {
		t.Fatalf("no worker: %d", w.Code)
	}
}

func TestAppProxyForwardsAndReplacesHeaders(t *testing.T) {
	f := newWorkerFake(t)
	var seen http.Header
	var seenHost, seenPath string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, seenHost, seenPath = r.Header.Clone(), r.Host, r.URL.RequestURI()
		w.Header().Set("X-App", "yes")
		_, _ = io.WriteString(w, "hola")
	}))
	defer backend.Close()
	f.set("mi-web", 200, fmt.Sprintf(`{"host_port":%d}`, backendPort(t, backend)))
	p := &AppProxy{WorkerSocket: f.sock}
	w := get(p, "mi-web.localhost:7321", "/a/b?q=1", map[string]string{
		"X-Forwarded-For": "6.6.6.6", "X-Forwarded-Host": "evil.example", "X-Forwarded-Proto": "https", "Forwarded": "for=6.6.6.6",
	})
	if w.Code != 200 || w.Body.String() != "hola" || w.Header().Get("X-App") != "yes" {
		t.Fatalf("%d %q", w.Code, w.Body)
	}
	if seenHost != "mi-web.localhost:7321" || seenPath != "/a/b?q=1" {
		t.Fatalf("host %q path %q", seenHost, seenPath)
	}
	if seen.Get("X-Forwarded-For") != "127.0.0.1" || seen.Get("X-Forwarded-Host") != "mi-web.localhost:7321" || seen.Get("X-Forwarded-Proto") != "http" || seen.Get("Forwarded") != "" {
		t.Fatalf("forwarded headers: %v", seen)
	}
}

func TestAppProxyCacheExpires(t *testing.T) {
	f := newWorkerFake(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") }))
	defer backend.Close()
	f.set("mi-web", 200, fmt.Sprintf(`{"host_port":%d}`, backendPort(t, backend)))
	now := time.Now()
	p := &AppProxy{WorkerSocket: f.sock, now: func() time.Time { return now }}
	for i := 0; i < 3; i++ {
		if w := get(p, "mi-web.localhost", "/", nil); w.Code != 200 {
			t.Fatal(w.Code)
		}
	}
	if f.asked.Load() != 1 {
		t.Fatalf("asked %d times", f.asked.Load())
	}
	// A failure is remembered no longer than a success.
	f.set("mi-web", 503, `{}`)
	now = now.Add(routeTTL - time.Millisecond)
	if w := get(p, "mi-web.localhost", "/", nil); w.Code != 200 {
		t.Fatal("expired early")
	}
	now = now.Add(2 * time.Millisecond)
	if w := get(p, "mi-web.localhost", "/", nil); w.Code != 503 || f.asked.Load() != 2 {
		t.Fatalf("after expiry: %d, asked %d", w.Code, f.asked.Load())
	}
	f.set("mi-web", 200, fmt.Sprintf(`{"host_port":%d}`, backendPort(t, backend)))
	now = now.Add(routeTTL + time.Millisecond)
	if w := get(p, "mi-web.localhost", "/", nil); w.Code != 200 {
		t.Fatal("did not recover")
	}
}

func TestAppProxyCancelledClientDoesNotPoisonTheCache(t *testing.T) {
	f := newWorkerFake(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") }))
	defer backend.Close()
	f.set("mi-web", 200, fmt.Sprintf(`{"host_port":%d}`, backendPort(t, backend)))
	p := &AppProxy{WorkerSocket: f.sock}
	p.init()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := p.route(ctx, "mi-web"); e.port != 0 {
		t.Fatalf("cancelled lookup returned port %d", e.port)
	}
	if w := get(p, "mi-web.localhost", "/", nil); w.Code != 200 {
		t.Fatalf("next request saw the cancelled result: %d", w.Code)
	}
}

func TestAppProxyWebSocketUpgrade(t *testing.T) {
	f := newWorkerFake(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "no upgrade", 400)
			return
		}
		c, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		_ = rw.Flush()
		line, _ := rw.ReadString('\n')
		_, _ = rw.WriteString("echo:" + line)
		_ = rw.Flush()
	}))
	defer backend.Close()
	f.set("socket", 200, fmt.Sprintf(`{"host_port":%d}`, backendPort(t, backend)))
	front := httptest.NewServer(&AppProxy{WorkerSocket: f.sock})
	defer front.Close()
	c, err := net.Dial("tcp", front.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(c, "GET /ws HTTP/1.1\r\nHost: socket.localhost:7321\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n")
	br := bufio.NewReader(c)
	status, err := br.ReadString('\n')
	if err != nil || !strings.Contains(status, "101") {
		t.Fatalf("status %q %v", status, err)
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
	}
	fmt.Fprint(c, "ping\n")
	got, err := br.ReadString('\n')
	if err != nil || got != "echo:ping\n" {
		t.Fatalf("%q %v", got, err)
	}
}

// The application listener serves applications only: API paths are not
// reachable, even with a Host that looks like the main server.
func TestAppProxyDoesNotServeTheAPI(t *testing.T) {
	f := newWorkerFake(t)
	called := false
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	}))
	defer backend.Close()
	f.set("mi-web", 200, fmt.Sprintf(`{"host_port":%d}`, backendPort(t, backend)))
	srv := (&AppProxy{WorkerSocket: f.sock}).Server("127.0.0.1:0")
	if srv.WriteTimeout != 0 || srv.ReadHeaderTimeout == 0 {
		t.Fatal("server timeouts")
	}
	h := srv.Handler
	for _, host := range []string{"127.0.0.1:7300", "localhost:7300", "127.0.0.1:7321"} {
		for _, path := range []string{"/api/v1/status", "/api/v1/capabilities", "/api/v1/worker/routes/mi-web", "/api/v1/auth/login"} {
			if w := get(h, host, path, nil); w.Code != 404 || strings.Contains(w.Body.String(), "mode") {
				t.Errorf("%s%s: %d %s", host, path, w.Code, w.Body)
			}
		}
	}
	if f.asked.Load() != 0 || called {
		t.Fatal("API host reached a backend")
	}
	// Under an application host, the path belongs to the application.
	if w := get(h, "mi-web.localhost", "/api/v1/status", nil); w.Code != 200 || !called {
		t.Fatalf("app path: %d", w.Code)
	}
}
