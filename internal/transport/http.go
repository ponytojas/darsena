package transport

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ponytojas/darsena/internal/modules/demo"
)

type API struct{ Store *demo.Store }

func send(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func problem(w http.ResponseWriter, status int, message string) {
	send(w, status, map[string]string{"error": message})
}
func read(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Header.Get("Content-Type") != "application/json" {
		problem(w, 415, "Se requiere Content-Type: application/json.")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		problem(w, 400, "JSON inválido o campos desconocidos.")
		return false
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		problem(w, 400, "La petición debe contener un único objeto JSON.")
		return false
	}
	return true
}
func (a API) Handler(webDir string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/status", func(w http.ResponseWriter, r *http.Request) {
		send(w, 200, map[string]any{"mode": "demo", "persistent": false})
	})
	mux.HandleFunc("GET /api/v1/applications", func(w http.ResponseWriter, r *http.Request) { send(w, 200, a.Store.Apps()) })
	mux.HandleFunc("GET /api/v1/applications/{id}", func(w http.ResponseWriter, r *http.Request) {
		app, ok := a.Store.App(r.PathValue("id"))
		if !ok {
			problem(w, 404, "Aplicación no encontrada.")
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
			problem(w, 400, err.Error())
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
			problem(w, 404, "Aplicación no encontrada.")
			return
		}
		op, err := a.Store.Deploy(r.PathValue("id"), in.Scenario)
		if err != nil {
			problem(w, 409, err.Error())
			return
		}
		send(w, 202, op)
	})
	mux.HandleFunc("GET /api/v1/operations", func(w http.ResponseWriter, r *http.Request) { send(w, 200, a.Store.Operations()) })
	mux.HandleFunc("GET /api/v1/operations/{id}", func(w http.ResponseWriter, r *http.Request) {
		op, ok := a.Store.Operation(r.PathValue("id"))
		if !ok {
			problem(w, 404, "Operación no encontrada.")
			return
		}
		send(w, 200, op)
	})
	mux.HandleFunc("GET /api/v1/operations/{id}/events", a.events)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { problem(w, 404, "Ruta API no encontrada.") })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
        if r.Method != http.MethodGet && r.Method != http.MethodHead { w.Header().Set("Allow", "GET, HEAD"); http.Error(w, "Method not allowed", http.StatusMethodNotAllowed); return }
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
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		mux.ServeHTTP(w, r)
	})
}
func (a API) events(w http.ResponseWriter, r *http.Request) {
	op, ok := a.Store.Operation(r.PathValue("id"))
	if !ok {
		problem(w, 404, "Operación no encontrada.")
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
