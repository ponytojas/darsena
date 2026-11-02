package transport

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"
)

type requestLogKey struct{}
type requestLog struct{ id, workerID string }

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status != 0 {
		return
	}
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}
func (w *statusWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

type flushStatusWriter struct {
	*statusWriter
	flusher http.Flusher
}

func (w *flushStatusWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	w.flusher.Flush()
}

// logRequests records only a server-owned route pattern, generated ID, status and duration.
// It deliberately does not record user-provided paths, headers, bodies or query strings.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err != nil {
			problem(w, http.StatusServiceUnavailable, "Servicio no disponible.")
			return
		}
		trace := &requestLog{id: hex.EncodeToString(raw[:])}
		r = r.WithContext(context.WithValue(r.Context(), requestLogKey{}, trace))
		w.Header().Set("X-Request-ID", trace.id)
		sw := &statusWriter{ResponseWriter: w}
		var out http.ResponseWriter = sw
		if f, ok := w.(http.Flusher); ok {
			out = &flushStatusWriter{statusWriter: sw, flusher: f}
		}
		started := time.Now()
		defer func() {
			pattern := r.Pattern
			if pattern == "" {
				pattern = "rejected"
			}
			status := sw.status
			if status == 0 {
				status = http.StatusOK
			}
			attrs := []any{"component", "server", "request_id", trace.id, "route", pattern, "status", status, "duration_ms", time.Since(started).Milliseconds()}
			if trace.workerID != "" {
				attrs = append(attrs, "worker_request_id", trace.workerID)
			}
			slog.Info("http_request", attrs...)
		}()
		next.ServeHTTP(out, r)
	})
}

func linkWorkerResponse(resp *http.Response) error {
	trace, _ := resp.Request.Context().Value(requestLogKey{}).(*requestLog)
	if trace == nil {
		return nil
	}
	workerID := resp.Header.Get("X-Request-ID")
	if len(workerID) == 32 {
		if _, err := hex.DecodeString(workerID); err == nil {
			trace.workerID = workerID
		}
	}
	resp.Header.Set("X-Request-ID", trace.id)
	return nil
}
