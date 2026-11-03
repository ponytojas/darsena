// Package ownerauth provides an isolated, in-memory session boundary for a
// future loopback-only owner interface. It is not wired to the demo server.
package ownerauth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ponytojas/darsena/internal/platform/privatefile"
	"golang.org/x/sys/unix"
)

const (
	cookieName      = "darsena_owner"
	csrfHeader      = "X-Darsena-CSRF"
	tokenBytes      = 32
	maxSessions     = 128
	sessionLifetime = 30 * time.Minute
)

var errConfiguration = errors.New("invalid owner authentication configuration")

type session struct {
	csrf    [32]byte
	expires time.Time
}

// Manager must not be copied. Restarting the process revokes every session.
type Manager struct {
	mu         sync.Mutex
	credential [32]byte
	origin     string
	host       string
	sessions   map[[32]byte]session
	now        func() time.Time
}

// New loads a 32-byte random credential encoded as unpadded base64url from a
// private regular file. Ancestor checks are not atomic and do not inspect ACLs;
// operators must ensure that the path cannot be replaced by other users.
// Do not pass the credential in process args.
func New(path, origin string) (*Manager, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.String() != origin {
		return nil, errConfiguration
	}
	host, port, err := net.SplitHostPort(u.Host)
	portNumber, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || portNumber < 1 || portNumber > 65535 || (host != "localhost" && !isLoopback(host)) {
		return nil, errConfiguration
	}
	if !privatefile.SafeAncestors(path) {
		return nil, errConfiguration
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, errConfiguration
	}
	f := os.NewFile(uintptr(fd), "credential")
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 44 {
		return nil, errConfiguration
	}
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Uid != uint32(os.Geteuid()) {
		return nil, errConfiguration
	}
	raw, err := io.ReadAll(io.LimitReader(f, 45))
	if err != nil {
		return nil, errConfiguration
	}
	encoded := strings.TrimSuffix(string(raw), "\n")
	credential, err := decodeToken(encoded)
	if err != nil {
		return nil, errConfiguration
	}
	return &Manager{credential: sha256.Sum256(credential), origin: origin, host: u.Host, sessions: make(map[[32]byte]session), now: time.Now}, nil
}

// Generate writes a cryptographically random credential to a new private file.
// Ancestor checks do not inspect ACLs and are not atomic against replacement.
// It never returns or prints the credential or overwrites an existing file.
func Generate(path string) error {
	credential, err := randomToken()
	if err != nil {
		return errConfiguration
	}
	if privatefile.Create(path, []byte(credential+"\n")) != nil {
		return errConfiguration
	}
	return nil
}

func isLoopback(host string) bool {
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

func decodeToken(s string) ([]byte, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) != tokenBytes || base64.RawURLEncoding.EncodeToString(b) != s {
		return nil, errConfiguration
	}
	return b, nil
}

func randomToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", errConfiguration
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (m *Manager) sameOrigin(r *http.Request) bool {
	return r.Host == m.host && len(r.Header.Values("Origin")) == 1 && r.Header.Get("Origin") == m.origin
}

func fail(w http.ResponseWriter, status int) {
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, http.StatusText(status), status)
}

// Login consumes a credential from a JSON POST body, never from a URL. The
// returned CSRF token is held in memory by the client, not browser storage.
func (m *Manager) Login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !m.sameOrigin(r) {
		fail(w, http.StatusForbidden)
		return
	}
	if r.Header.Get("Content-Type") != "application/json" {
		fail(w, http.StatusUnsupportedMediaType)
		return
	}
	var input struct {
		Credential string `json:"credential"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	dec.DisallowUnknownFields()
	if dec.Decode(&input) != nil || dec.Decode(new(any)) != io.EOF {
		fail(w, http.StatusBadRequest)
		return
	}
	credential, err := decodeToken(input.Credential)
	if err != nil {
		fail(w, http.StatusUnauthorized)
		return
	}
	digest := sha256.Sum256(credential)
	if subtle.ConstantTimeCompare(digest[:], m.credential[:]) != 1 {
		fail(w, http.StatusUnauthorized)
		return
	}
	sid, err := randomToken()
	if err != nil {
		fail(w, http.StatusServiceUnavailable)
		return
	}
	csrf, err := randomToken()
	if err != nil {
		fail(w, http.StatusServiceUnavailable)
		return
	}
	csrfBytes, _ := decodeToken(csrf)
	sidHash := sha256.Sum256([]byte(sid))
	csrfHash := sha256.Sum256(csrfBytes)
	now := m.now()
	m.mu.Lock()
	for key, s := range m.sessions {
		if !now.Before(s.expires) {
			delete(m.sessions, key)
		}
	}
	if len(m.sessions) >= maxSessions {
		m.mu.Unlock()
		fail(w, http.StatusServiceUnavailable)
		return
	}
	m.sessions[sidHash] = session{csrf: csrfHash, expires: now.Add(sessionLifetime)}
	m.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: sid, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: int(sessionLifetime.Seconds())})
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		CSRF string `json:"csrf"`
	}{csrf})
}

// Require requires a per-session token on all methods. Cookies are scoped to
// hostname, not port, so a cookie alone is insufficient to authorize even a
// read: another local service on the same hostname could receive that cookie.
// Browser clients must use same-origin fetch and retain the token only in memory.
// Mutations also require the exact Origin. The caller must not mutate on GET/HEAD.
func (m *Manager) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != m.host {
			fail(w, http.StatusForbidden)
			return
		}
		c, err := r.Cookie(cookieName)
		if err != nil {
			fail(w, http.StatusUnauthorized)
			return
		}
		if _, err := decodeToken(c.Value); err != nil {
			fail(w, http.StatusUnauthorized)
			return
		}
		key := sha256.Sum256([]byte(c.Value))
		m.mu.Lock()
		s, ok := m.sessions[key]
		if ok && !m.now().Before(s.expires) {
			delete(m.sessions, key)
			ok = false
		}
		m.mu.Unlock()
		if !ok {
			fail(w, http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if !m.sameOrigin(r) {
				fail(w, http.StatusForbidden)
				return
			}
		} else if origins := r.Header.Values("Origin"); len(origins) > 0 && (len(origins) != 1 || origins[0] != m.origin) {
			fail(w, http.StatusForbidden)
			return
		}
		if len(r.Header.Values(csrfHeader)) != 1 {
			fail(w, http.StatusForbidden)
			return
		}
		csrf, err := decodeToken(r.Header.Get(csrfHeader))
		if err != nil {
			fail(w, http.StatusForbidden)
			return
		}
		digest := sha256.Sum256(csrf)
		if subtle.ConstantTimeCompare(digest[:], s.csrf[:]) != 1 {
			fail(w, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Logout must be served through Require so that revocation is CSRF protected.
func (m *Manager) Logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, http.StatusMethodNotAllowed)
		return
	}
	c, err := r.Cookie(cookieName)
	if err != nil {
		fail(w, http.StatusUnauthorized)
		return
	}
	key := sha256.Sum256([]byte(c.Value))
	m.mu.Lock()
	delete(m.sessions, key)
	m.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: cookieName, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}
