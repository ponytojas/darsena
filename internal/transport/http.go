package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ponytojas/darsena/internal/modules/demo"
	"github.com/ponytojas/darsena/internal/platform/ownerauth"
)

type API struct {
	Store        *demo.Store
	WorkerSocket string
	Owner        *ownerauth.Manager
	Installation string
	// AppsPort is the port of the application listener; zero when disabled.
	AppsPort int
}

func send(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func problem(w http.ResponseWriter, status int, message string) {
	send(w, status, map[string]string{"error": message})
}

// problemCode adds a stable code so clients can translate the message.
func problemCode(w http.ResponseWriter, status int, code, message string) {
	send(w, status, map[string]string{"error": message, "code": code})
}
func read(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Header.Get("Content-Type") != "application/json" {
		problemCode(w, 415, "unsupported_media_type", "Se requiere Content-Type: application/json.")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		problemCode(w, 400, "invalid_json", "JSON inválido o campos desconocidos.")
		return false
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		problemCode(w, 400, "single_json_object", "La petición debe contener un único objeto JSON.")
		return false
	}
	return true
}
func (a API) Handler(webDir string) http.Handler {
	mux := http.NewServeMux()
	if a.WorkerSocket != "" {
		headerTimeout := 5 * time.Second
		if a.Owner != nil {
			headerTimeout = 90 * time.Second
		}
		proxy := &httputil.ReverseProxy{Director: func(r *http.Request) {
			r.URL.Scheme = "http"
			r.URL.Host = "worker"
			r.Host = "worker"
			r.URL.Path = strings.TrimPrefix(r.URL.Path, "/api/v1/worker")
			r.URL.RawPath = ""
			r.Header.Del("Cookie")
			r.Header.Del("Authorization")
			r.Header.Del("X-Darsena-CSRF")
			r.Header.Del("Origin")
		}, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", a.WorkerSocket)
		}, ResponseHeaderTimeout: headerTimeout}, ModifyResponse: func(resp *http.Response) error { resp.Header.Del("Set-Cookie"); return linkWorkerResponse(resp) }, ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) { problem(w, 503, "Worker no disponible.") }}
		mux.Handle("/api/v1/worker/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// A demo server must never forward real-service operations, even if
			// inadvertently connected to an experimental worker.
			for _, part := range strings.Split(r.URL.Path, "/") {
				if (part == "service" || part == "pull") && a.Owner == nil {
					problem(w, http.StatusForbidden, "Operación no disponible.")
					return
				}
				// Routes carry the host port of an application. They are for the
				// application proxy only and never reach a browser.
				if part == "routes" {
					problemCode(w, http.StatusNotFound, "not_found", "Ruta API no encontrada.")
					return
				}
			}
			if a.Owner != nil && !a.experimentalWorkerMatches(r.Context()) {
				problem(w, http.StatusServiceUnavailable, "Worker experimental no disponible.")
				return
			}
			if a.Owner != nil && r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/service") && !a.experimentalRuntimeReady(r.Context()) {
				problem(w, http.StatusServiceUnavailable, "Runtime no disponible.")
				return
			}
			http.MaxBytesHandler(proxy, 100000).ServeHTTP(w, r)
		}))
	}
	if a.Owner != nil {
		mux.HandleFunc("POST /api/v1/auth/login", a.Owner.Login)
		mux.Handle("POST /api/v1/auth/logout", a.Owner.Require(http.HandlerFunc(a.Owner.Logout)))
	}
	mux.HandleFunc("GET /api/v1/status", func(w http.ResponseWriter, r *http.Request) {
		mode := "demo"
		if a.Owner != nil {
			mode = "experimental"
		}
		send(w, 200, map[string]any{"mode": mode, "persistent": false, "worker_enabled": a.WorkerSocket != ""})
	})
	mux.HandleFunc("GET /api/v1/capabilities", a.capabilities)
	mux.HandleFunc("GET /api/v1/health/live", func(w http.ResponseWriter, r *http.Request) {
		send(w, http.StatusOK, map[string]string{"status": "alive"})
	})
	mux.HandleFunc("GET /api/v1/health/ready", func(w http.ResponseWriter, r *http.Request) {
		workerStatus := "disabled"
		status := http.StatusOK
		if a.WorkerSocket != "" {
			workerStatus = "unavailable"
			ctx, cancel := context.WithTimeout(r.Context(), time.Second)
			defer cancel()
			transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", a.WorkerSocket)
			}}
			defer transport.CloseIdleConnections()
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://worker/readyz", nil)
			resp, err := (&http.Client{Transport: transport}).Do(req)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					workerStatus = "ready"
				}
			}
			if workerStatus != "ready" {
				status = http.StatusServiceUnavailable
			}
		}
		mode := "demo"
		if a.Owner != nil {
			mode = "experimental"
			if !a.experimentalWorkerMatches(r.Context()) {
				workerStatus = "unavailable"
				status = http.StatusServiceUnavailable
			}
		}
		send(w, status, map[string]string{"mode": mode, "worker": workerStatus})
	})
	mux.HandleFunc("GET /api/v1/applications", func(w http.ResponseWriter, r *http.Request) { send(w, 200, a.Store.Apps()) })
	mux.HandleFunc("GET /api/v1/applications/{id}", func(w http.ResponseWriter, r *http.Request) {
		app, ok := a.Store.App(r.PathValue("id"))
		if !ok {
			problemCode(w, 404, "application_not_found", "Aplicación no encontrada.")
			return
		}
		send(w, 200, app)
	})
	mux.HandleFunc("POST /api/v1/applications", func(w http.ResponseWriter, r *http.Request) {
		var in demo.CreateInput
		if !read(w, r, &in) {
			return
		}
		app, op, err := a.Store.Create(in)
		if err != nil {
			problemCode(w, 400, demo.ErrorCode(err), err.Error())
			return
		}
		send(w, 202, map[string]any{"application": app, "operation": op})
	})
	mux.HandleFunc("POST /api/v1/applications/{id}/deployments", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Scenario string `json:"scenario"`
		}
		if !read(w, r, &in) {
			return
		}
		if _, ok := a.Store.App(r.PathValue("id")); !ok {
			problemCode(w, 404, "application_not_found", "Aplicación no encontrada.")
			return
		}
		op, err := a.Store.Deploy(r.PathValue("id"), in.Scenario)
		if err != nil {
			problemCode(w, 409, demo.ErrorCode(err), err.Error())
			return
		}
		send(w, 202, op)
	})
	mux.HandleFunc("GET /api/v1/operations", func(w http.ResponseWriter, r *http.Request) { send(w, 200, a.Store.Operations()) })
	mux.HandleFunc("GET /api/v1/operations/{id}", func(w http.ResponseWriter, r *http.Request) {
		op, ok := a.Store.Operation(r.PathValue("id"))
		if !ok {
			problemCode(w, 404, "operation_not_found", "Operación no encontrada.")
			return
		}
		send(w, 200, op)
	})
	mux.HandleFunc("GET /api/v1/operations/{id}/events", a.events)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { problem(w, 404, "Ruta API no encontrada.") })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if webDir == "" {
			http.Error(w, "Build the frontend or start Vite; see README.", 404)
			return
		}
		clean := filepath.Clean("/" + r.URL.Path)
		path := filepath.Join(webDir, clean)
		if st, err := os.Stat(path); err != nil || st.IsDir() {
			path = filepath.Join(webDir, "index.html")
		}
		http.ServeFile(w, r, path)
	})
	return logRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			problem(w, 403, "La demostración solo permite acceso local.")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || u.Host != r.Host {
				problem(w, 403, "Origen no permitido.")
				return
			}
		}
		if a.Owner != nil && strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/api/v1/auth/login" && r.URL.Path != "/api/v1/capabilities" && r.URL.Path != "/api/v1/health/live" {
			a.Owner.Require(mux).ServeHTTP(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	}))
}
func (a API) events(w http.ResponseWriter, r *http.Request) {
	op, ok := a.Store.Operation(r.PathValue("id"))
	if !ok {
		problemCode(w, 404, "operation_not_found", "Operación no encontrada.")
		return
	}
	f, ok := w.(http.Flusher)
	if !ok {
		problem(w, 500, "Streaming no disponible.")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	// Always send a current snapshot, including on reconnect. No unbounded replay queue.
	revision := -1
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	heartbeat := time.NewTicker(10 * time.Second)
	defer heartbeat.Stop()
	for {
		if op.Revision != revision {
			_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(5 * time.Second))
			b, _ := json.Marshal(op)
			if _, err := fmt.Fprintf(w, "id: %d\nevent: snapshot\ndata: %s\n\n", op.Revision, b); err != nil {
				return
			}
			f.Flush()
			revision = op.Revision
			if op.Status == "succeeded" || op.Status == "failed" || op.Status == "interrupted" {
				return
			}
		}
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			f.Flush()
		case <-ticker.C:
			op, ok = a.Store.Operation(op.ID)
			if !ok {
				return
			}
		}
	}
}
func LoopbackAddress(addr string) bool {
	h, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(strings.Trim(h, "[]"))
	return ip != nil && ip.IsLoopback()
}
