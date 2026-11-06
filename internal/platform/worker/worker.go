// Package worker exposes a restricted local demonstration job service.
package worker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ponytojas/darsena/internal/platform/configvault"
	"github.com/ponytojas/darsena/internal/platform/jobs"
	rt "github.com/ponytojas/darsena/internal/platform/runtime"
	"golang.org/x/sys/unix"
)

type Service struct {
	store    *jobs.Store
	listener net.Listener
	lock     *os.File
	socket   string
	server   *http.Server
	mux      *http.ServeMux
	vault    *configvault.Vault
	runtime  *realRuntime
	// lifecycle orders application deletion against configuration writes:
	// writers hold the read side and re-check the application inside it;
	// deletion holds the write side while purging configuration and the row.
	lifecycle sync.RWMutex
	// intentGuard is the path of a runtime intention database that this process
	// does not have open. See runtimeKnowledge.
	intentGuard string
	// beforeGate is a test hook run between the identity check and the gate.
	beforeGate func()
}
type Request struct {
	ResourceID     string `json:"resource_id"`
	IdempotencyKey string `json:"idempotency_key"`
}
type Response struct {
	ID         string    `json:"id"`
	ResourceID string    `json:"resource_id"`
	Kind       string    `json:"kind"`
	Status     string    `json:"status"`
	Error      string    `json:"error"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func response(j jobs.Job) Response {
	return Response{j.ID, j.ResourceID, j.Kind, j.Status, j.Error, j.CreatedAt, j.UpdatedAt}
}
func send(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
func failure(w http.ResponseWriter, code int, message string) {
	send(w, code, map[string]string{"error": message})
}

// failureCode adds a stable machine-readable code the web client translates.
func failureCode(w http.ResponseWriter, status int, code, message string) {
	send(w, status, map[string]string{"error": message, "code": code})
}

// Open takes an exclusive advisory lock before recovering interrupted jobs.
// The socket's parent must already be private to this user.
func Open(ctx context.Context, dbPath, socket string) (*Service, error) {
	if dbPath == "" || dbPath == ":memory:" || strings.HasPrefix(strings.ToLower(dbPath), "file:") {
		return nil, errors.New("a persistent database path is required")
	}
	dbPath, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, err
	}
	socket, err = filepath.Abs(socket)
	if err != nil {
		return nil, err
	}
	parent := filepath.Dir(socket)
	st, err := os.Stat(parent)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("socket directory must be private (0700)")
	}
	if err = os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
		return nil, err
	}
	// Refuse links atomically and verify the opened inode before taking the lock.
	fd, err := unix.Open(dbPath+".lock", unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, errors.New("cannot open private worker lock")
	}
	lock := os.NewFile(uintptr(fd), "worker lock")
	lockInfo, err := lock.Stat()
	if err != nil || !lockInfo.Mode().IsRegular() || lockInfo.Mode().Perm()&0077 != 0 {
		lock.Close()
		return nil, errors.New("worker lock must be a private regular file")
	}
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("another worker owns the database: %w", err)
	}
	cleanup := func() { _ = unix.Flock(int(lock.Fd()), unix.LOCK_UN); _ = lock.Close() }
	store, err := jobs.Open(ctx, dbPath)
	if err != nil {
		cleanup()
		return nil, err
	}
	if _, err = os.Lstat(socket); err == nil {
		store.Close()
		cleanup()
		return nil, fmt.Errorf("socket already exists: %s", socket)
	} else if !errors.Is(err, os.ErrNotExist) {
		store.Close()
		cleanup()
		return nil, err
	}
	ln, err := net.Listen("unix", socket)
	if err != nil {
		store.Close()
		cleanup()
		return nil, err
	}
	fail := func(err error) (*Service, error) {
		ln.Close()
		os.Remove(socket)
		store.Close()
		cleanup()
		return nil, err
	}
	if err = os.Chmod(socket, 0600); err != nil {
		return fail(err)
	}
	var interrupted int
	for {
		ids, err := store.InterruptRunningBatch(ctx)
		if err != nil {
			return fail(err)
		}
		if len(ids) == 0 {
			break
		}
		interrupted += len(ids)
		for _, id := range ids {
			if safeJobID(id) {
				slog.Info("job_interrupted", "component", "worker", "job_id", id, "status", "interrupted", "reason", "worker_restart")
			} else {
				slog.Warn("job_interrupted", "component", "worker", "status", "interrupted", "reason", "invalid_job_id")
			}
		}
	}
	if interrupted > 0 {
		slog.Info("jobs_interrupted", "component", "worker", "count", interrupted)
	}
	s := &Service{store: store, listener: ln, lock: lock, socket: socket}
	mux := http.NewServeMux()
	s.mux = mux
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		send(w, 200, map[string]any{"mode": "simulation", "config_enabled": s.vault != nil})
	})
	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, r *http.Request) {
		send(w, 200, map[string]string{"status": "alive"})
	})
	mux.HandleFunc("GET /readyz", s.ready)
	mux.HandleFunc("POST /jobs", s.enqueue)
	mux.HandleFunc("GET /jobs", s.list)
	mux.HandleFunc("GET /jobs/{id}", s.get)
	mux.HandleFunc("GET /image", s.checkImage)
	mux.HandleFunc("GET /domains/check", s.checkDomain)
	mux.HandleFunc("GET /applications", s.listApplications)
	mux.HandleFunc("GET /applications/{id}", s.getApplication)
	mux.HandleFunc("POST /applications", s.createApplication)
	mux.HandleFunc("POST /applications/assign", s.assignApplications)
	mux.HandleFunc("PATCH /applications/{id}", s.updateApplication)
	mux.HandleFunc("DELETE /applications/{id}", s.deleteApplication)
	mux.HandleFunc("GET /projects", s.listProjects)
	mux.HandleFunc("POST /projects", s.createProject)
	mux.HandleFunc("PATCH /projects/{id}", s.updateProject)
	mux.HandleFunc("DELETE /projects/{id}", s.deleteProject)
	s.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 90 * time.Second, IdleTimeout: 15 * time.Second, MaxHeaderBytes: 8 << 10}
	return s, nil
}

// Only internally generated IDs are safe to include in operational logs.
func safeJobID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func logJob(level slog.Level, event, id string, args ...any) {
	fields := []any{"component", "worker"}
	if safeJobID(id) {
		fields = append(fields, "job_id", id)
	} else {
		fields = append(fields, "reason", "invalid_job_id")
	}
	fields = append(fields, args...)
	slog.Log(context.Background(), level, event, fields...)
}

func (s *Service) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	state, vault := "ready", "disabled"
	if s.store.Check(ctx) != nil {
		state = "unavailable"
	}
	if s.vault != nil {
		vault = "ready"
		if s.vault.Check(ctx) != nil {
			vault = "unavailable"
		}
	}
	status := http.StatusOK
	if state != "ready" || vault == "unavailable" {
		status = http.StatusServiceUnavailable
	}
	mode := "simulation"
	if s.runtime != nil {
		mode = "experimental"
	}
	send(w, status, map[string]string{"mode": mode, "state": state, "vault": vault})
}
func (s *Service) enqueue(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Content-Type") != "application/json" {
		failure(w, 415, "JSON required")
		return
	}
	var req Request
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&req); err != nil {
		failure(w, 400, "invalid request")
		return
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		failure(w, 400, "unexpected data")
		return
	}
	if strings.TrimSpace(req.ResourceID) == "" || len(req.ResourceID) > 128 || strings.TrimSpace(req.IdempotencyKey) == "" || len(req.IdempotencyKey) > 200 {
		failure(w, 400, "resource_id and idempotency_key are required")
		return
	}
	j, err := s.store.Enqueue(r.Context(), req.ResourceID, "simulation", req.IdempotencyKey)
	if err != nil {
		if errors.Is(err, jobs.ErrConflict) {
			failure(w, 409, "operation conflicts with an existing request")
		} else {
			failure(w, 500, "unable to enqueue request")
		}
		return
	}
	var requestID [16]byte
	if _, err := rand.Read(requestID[:]); err == nil {
		id := hex.EncodeToString(requestID[:])
		w.Header().Set("X-Request-ID", id)
		logJob(slog.LevelInfo, "job_enqueued", j.ID, "request_id", id, "status", j.Status)
	} else {
		logJob(slog.LevelInfo, "job_enqueued", j.ID, "status", j.Status)
	}
	send(w, 202, response(j))
}
func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.List(r.Context(), 100)
	if err != nil {
		failure(w, 500, "unable to list jobs")
		return
	}
	out := make([]Response, 0, len(list))
	for _, j := range list {
		out = append(out, response(j))
	}
	send(w, 200, out)
}
func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	j, err := s.store.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, jobs.ErrNotFound) {
		failure(w, 404, "job not found")
		return
	}
	if err != nil {
		failure(w, 500, "unable to read job")
		return
	}
	send(w, 200, response(j))
}

// Run processes simulated jobs. Experimental service handlers are registered
// only when EnableRuntime is explicitly called before Run.
func (s *Service) Run(ctx context.Context) error {
	done := make(chan error, 1)
	go func() { done <- s.server.Serve(s.listener) }()
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.server.Shutdown(stop); err != nil {
			_ = s.server.Close()
		}
	}()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			slog.Error("worker_server_failed", "component", "worker")
			return errors.New("worker server unavailable")
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			j, err := s.store.ClaimNext(ctx)
			if errors.Is(err, jobs.ErrNotFound) {
				continue
			}
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				slog.Error("job_claim_failed", "component", "worker")
				return errors.New("unable to claim job")
			}
			started := time.Now()
			logJob(slog.LevelInfo, "job_claimed", j.ID, "status", "running")
			if j.Kind != "simulation" {
				_, err = s.store.Complete(ctx, j.ID, "failed", "Unsupported operation.")
			} else {
				_, err = s.store.Complete(ctx, j.ID, "succeeded", "")
			}
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				logJob(slog.LevelError, "job_completion_failed", j.ID, "status", "running")
				return errors.New("unable to complete job")
			}
			status := "succeeded"
			if j.Kind != "simulation" {
				status = "failed"
			}
			logJob(slog.LevelInfo, "job_completed", j.ID, "status", status, "duration_ms", time.Since(started).Milliseconds())
		}
	}
}
func (s *Service) Close() error {
	_ = s.listener.Close()
	if s.runtime != nil {
		s.runtime.pulls.cancel()
		_ = s.runtime.store.Close()
	}
	if s.vault != nil {
		_ = s.vault.Close()
	}
	_ = os.Remove(s.socket)
	err := s.store.Close()
	_ = unix.Flock(int(s.lock.Fd()), unix.LOCK_UN)
	_ = s.lock.Close()
	return err
}

// EnableConfig attaches an encrypted experimental configuration store before Run.
func (s *Service) EnableConfig(v *configvault.Vault) error {
	if v == nil || s.vault != nil {
		return fmt.Errorf("configuration store already enabled or invalid")
	}
	s.vault = v
	s.mux.HandleFunc("GET /config/{resource}", func(w http.ResponseWriter, r *http.Request) {
		if !s.requireApplication(w, r) {
			return
		}
		result, err := v.Get(r.Context(), r.PathValue("resource"))
		if err != nil {
			configError(w, err)
			return
		}
		send(w, 200, result)
	})
	s.mux.HandleFunc("GET /config/{resource}/history", func(w http.ResponseWriter, r *http.Request) {
		if !s.requireApplication(w, r) {
			return
		}
		rows, err := v.HistoryEntries(r.Context(), r.PathValue("resource"))
		// An existing application with no configuration yet has an empty history.
		if err != nil && !errors.Is(err, configvault.ErrNotFound) {
			configError(w, err)
			return
		}
		// The vault lists oldest first; the API answers newest first.
		entries := make([]configvault.HistoryEntry, 0, len(rows))
		for i := len(rows) - 1; i >= 0; i-- {
			entries = append(entries, rows[i])
		}
		send(w, 200, map[string]any{"entries": entries})
	})
	s.mux.HandleFunc("POST /config/{resource}/variables/{name}/restore", func(w http.ResponseWriter, r *http.Request) {
		s.lifecycle.RLock()
		defer s.lifecycle.RUnlock()
		if !s.requireApplication(w, r) {
			return
		}
		var in struct {
			Revision     *int64 `json:"revision"`
			FromRevision *int64 `json:"from_revision"`
		}
		if !configBody(w, r, &in) {
			return
		}
		if in.Revision == nil || in.FromRevision == nil {
			failureCode(w, 400, "invalid_configuration", "revision and from_revision are required")
			return
		}
		if _, err := v.Restore(r.Context(), r.PathValue("resource"), *in.Revision, r.PathValue("name"), *in.FromRevision); err != nil {
			configError(w, err)
			return
		}
		result, err := v.Get(r.Context(), r.PathValue("resource"))
		if err != nil {
			configError(w, err)
			return
		}
		send(w, 200, result)
	})
	s.mux.HandleFunc("PUT /config/{resource}/variables/{name}", func(w http.ResponseWriter, r *http.Request) {
		s.lifecycle.RLock()
		defer s.lifecycle.RUnlock()
		if !s.requireApplication(w, r) {
			return
		}
		var in struct {
			Revision *int64  `json:"revision"`
			Value    *string `json:"value"`
			Secret   *bool   `json:"secret"`
		}
		if !configBody(w, r, &in) {
			return
		}
		if in.Revision == nil || in.Value == nil || in.Secret == nil {
			failure(w, 400, "revision, value and secret are required")
			return
		}
		result, err := v.Set(r.Context(), r.PathValue("resource"), *in.Revision, r.PathValue("name"), *in.Value, *in.Secret)
		if err != nil {
			configError(w, err)
			return
		}
		send(w, 200, result)
	})
	s.mux.HandleFunc("DELETE /config/{resource}/variables/{name}", func(w http.ResponseWriter, r *http.Request) {
		s.lifecycle.RLock()
		defer s.lifecycle.RUnlock()
		if !s.requireApplication(w, r) {
			return
		}
		var in struct {
			Revision *int64 `json:"revision"`
		}
		if !configBody(w, r, &in) {
			return
		}
		if in.Revision == nil {
			failure(w, 400, "revision is required")
			return
		}
		result, err := v.Delete(r.Context(), r.PathValue("resource"), *in.Revision, r.PathValue("name"))
		if err != nil {
			configError(w, err)
			return
		}
		send(w, 200, result)
	})
	return nil
}
func configBody(w http.ResponseWriter, r *http.Request, value any) bool {
	if r.Header.Get("Content-Type") != "application/json" {
		failure(w, 415, "JSON required")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 100000)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		failure(w, 400, "invalid request")
		return false
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		failure(w, 400, "unexpected data")
		return false
	}
	return true
}
func configError(w http.ResponseWriter, err error) {
	if errors.Is(err, configvault.ErrVariableAbsent) {
		failureCode(w, 409, "variable_not_in_revision", "variable is not in that revision")
	} else if errors.Is(err, configvault.ErrNotRetained) {
		failureCode(w, 409, "revision_not_retained", "revision is no longer retained")
	} else if errors.Is(err, configvault.ErrInvalid) {
		failureCode(w, 400, "invalid_configuration", "invalid configuration")
	} else if errors.Is(err, configvault.ErrConflict) {
		failureCode(w, 409, "configuration_conflict", "configuration changed; reload before saving")
	} else {
		failureCode(w, 500, "configuration_unavailable", "configuration unavailable")
	}
}

func (s *Service) createApplication(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name          string `json:"name"`
		Image         string `json:"image"`
		Port          int    `json:"port"`
		Access        string `json:"access"`
		SharedNetwork bool   `json:"shared_network"`
		Domain        string `json:"domain"`
	}
	if !configBody(w, r, &in) {
		return
	}
	image, ok := pinnedImage(w, in.Image)
	if !ok {
		return
	}
	access := jobs.AppAccess{Access: in.Access, SharedNetwork: in.SharedNetwork, Domain: in.Domain}
	if access.Access == jobs.AccessLocal && access.Domain == "" {
		// The default is the name; if that is not a valid domain the error
		// stands and the person must choose one.
		access.Domain = in.Name
	}
	app, err := s.store.CreateApplication(r.Context(), in.Name, image, in.Port, access)
	if accessFailure(w, err) {
		return
	}
	if errors.Is(err, jobs.ErrInvalid) {
		failureCode(w, 400, "invalid_application", "invalid application")
		return
	}
	if errors.Is(err, jobs.ErrConflict) {
		failure(w, 409, "application name already exists")
		return
	}
	if err != nil {
		failure(w, 500, "unable to create application")
		return
	}
	send(w, 201, app)
}

// accessFailure writes the coded failure for a domain error and reports whether
// it did.
func accessFailure(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, jobs.ErrDomainTaken):
		failureCode(w, 409, "domain_taken", "that domain is already in use")
	case errors.Is(err, jobs.ErrReservedDomain):
		failureCode(w, 400, "domain_reserved", "that domain is reserved")
	case errors.Is(err, jobs.ErrInvalidDomain):
		failureCode(w, 400, "invalid_domain", "invalid domain")
	default:
		return false
	}
	return true
}

// checkDomain is advisory: availability is decided by the store's unique index
// when an application is saved. The optional application parameter names an
// application whose own domain is not reported as taken.
func (s *Service) checkDomain(w http.ResponseWriter, r *http.Request) {
	type answer struct {
		Available bool   `json:"available"`
		Reason    string `json:"reason,omitempty"`
	}
	domain, err := jobs.NormalizeDomain(r.URL.Query().Get("name"))
	switch {
	case errors.Is(err, jobs.ErrReservedDomain):
		send(w, 200, answer{Reason: "reserved"})
		return
	case err != nil:
		send(w, 200, answer{Reason: "invalid"})
		return
	}
	owner, err := s.store.ApplicationByDomain(r.Context(), domain)
	if errors.Is(err, jobs.ErrNotFound) || (err == nil && owner.ID == r.URL.Query().Get("application")) {
		send(w, 200, answer{Available: true})
		return
	}
	if err != nil {
		failure(w, 500, "unable to check domain")
		return
	}
	send(w, 200, answer{Reason: "taken"})
}

// pinnedImage returns the canonical name[:tag]@digest form of a definition
// image, or writes invalid_image. Definitions always pin a digest (a tag alone
// is resolved through GET /image before saving); the tag is kept for display.
func pinnedImage(w http.ResponseWriter, raw string) (string, bool) {
	ref, err := rt.ParseImage(raw)
	if err != nil || ref.Digest == "" {
		failureCode(w, 400, "invalid_image", "image must be a valid reference pinned by sha256 digest")
		return "", false
	}
	return ref.Canonical(), true
}

func (s *Service) requireApplication(w http.ResponseWriter, r *http.Request) bool {
	_, err := s.store.Application(r.Context(), r.PathValue("resource"))
	if errors.Is(err, jobs.ErrNotFound) {
		failureCode(w, 404, "application_not_found", "application not found")
		return false
	}
	if err != nil {
		failure(w, 500, "application unavailable")
		return false
	}
	return true
}
