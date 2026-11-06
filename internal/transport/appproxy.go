package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ponytojas/darsena/internal/platform/jobs"
)

// routeTTL bounds how long a worker answer, success or failure, is reused.
const routeTTL = 2 * time.Second

// maxRouteCache bounds the cache; any local process can ask for valid labels.
const maxRouteCache = 1024

// Route outcomes other than a port.
const (
	routeUnknown = iota + 1 // no such domain
	routeDown               // known, but not running or not inspectable
)

type routeEntry struct {
	port    int // zero unless the service is running
	outcome int
	expires time.Time
}

// AppProxy routes <domain>.localhost to the loopback port of a running local
// application. It is a separate http.Handler: it shares nothing with the API
// handler or the owner session, and it never takes a target from the request.
// The only trusted input is the worker's answer for an exact, validated domain.
type AppProxy struct {
	// WorkerSocket is the private worker socket that answers GET /routes/{domain}.
	WorkerSocket string

	once   sync.Once
	client *http.Client
	proxy  *httputil.ReverseProxy
	now    func() time.Time

	mu    sync.Mutex
	cache map[string]routeEntry
}

type routeTarget struct{}

func (p *AppProxy) init() {
	p.once.Do(func() {
		if p.now == nil {
			p.now = time.Now
		}
		p.cache = map[string]routeEntry{}
		p.client = &http.Client{
			Timeout: 3 * time.Second,
			Transport: &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", p.WorkerSocket)
			}},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
		p.proxy = &httputil.ReverseProxy{
			Rewrite: func(r *httputil.ProxyRequest) {
				port, _ := r.In.Context().Value(routeTarget{}).(int)
				r.Out.URL.Scheme = "http"
				r.Out.URL.Host = net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
				// The application sees the name the browser used. Forwarding
				// headers sent by the client were already dropped by the proxy;
				// these describe this hop only.
				r.Out.Host = r.In.Host
				r.SetXForwarded()
			},
			Transport: &http.Transport{
				Proxy:                 nil,
				DialContext:           (&net.Dialer{Timeout: 3 * time.Second}).DialContext,
				MaxIdleConnsPerHost:   4,
				IdleConnTimeout:       30 * time.Second,
				ResponseHeaderTimeout: 2 * time.Minute,
			},
			ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
				appPage(w, http.StatusBadGateway, "The app is not answering yet", "The app is running but did not respond. It may still be starting. Try again in a moment.")
			},
		}
	})
}

// appDomain extracts the single label of an accepted host: exactly
// <label>.localhost, optionally followed by :<port>, with a label that is a
// valid, non-reserved domain. Anything else (bare localhost, several labels,
// IP addresses, other suffixes, a trailing dot) is refused. Letter case does not
// matter in a host name, so the label is lower-cased.
func appDomain(host string) (string, bool) {
	if i := strings.LastIndexByte(host, ':'); i >= 0 {
		port, err := strconv.Atoi(host[i+1:])
		if err != nil || port < 1 || port > 65535 || host[i+1:] != strconv.Itoa(port) {
			return "", false
		}
		host = host[:i]
	}
	label, ok := strings.CutSuffix(strings.ToLower(host), ".localhost")
	if !ok || strings.Contains(label, ".") {
		return "", false
	}
	domain, err := jobs.NormalizeDomain(label)
	if err != nil || domain != label {
		return "", false
	}
	return domain, true
}

func (p *AppProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.init()
	domain, ok := appDomain(r.Host)
	if !ok {
		appPage(w, http.StatusNotFound, "Unknown address", "No app uses this address.")
		return
	}
	entry := p.route(r.Context(), domain)
	switch {
	case entry.port > 0:
		p.proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), routeTarget{}, entry.port)))
	case entry.outcome == routeUnknown:
		appPage(w, http.StatusNotFound, "Unknown address", "No app uses this address.")
	default:
		appPage(w, http.StatusServiceUnavailable, "The app is not running", "This app is stopped or has not been started. Start it from Dársena and reload this page.")
	}
}

func (p *AppProxy) route(ctx context.Context, domain string) routeEntry {
	now := p.now()
	p.mu.Lock()
	if e, ok := p.cache[domain]; ok && now.Before(e.expires) {
		p.mu.Unlock()
		return e
	}
	p.mu.Unlock()
	e := p.ask(ctx, domain)
	// A client that went away says nothing about the route: keep it out of the
	// cache so other requests do not see a false "down".
	if ctx.Err() != nil {
		return e
	}
	e.expires = now.Add(routeTTL)
	p.mu.Lock()
	if len(p.cache) >= maxRouteCache {
		for k, v := range p.cache {
			if !now.Before(v.expires) {
				delete(p.cache, k)
			}
		}
	}
	if len(p.cache) < maxRouteCache {
		p.cache[domain] = e
	}
	p.mu.Unlock()
	return e
}

// ask queries the worker. A malformed or unexpected answer is "down", never a
// port.
func (p *AppProxy) ask(ctx context.Context, domain string) routeEntry {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://worker/routes/"+url.PathEscape(domain), nil)
	if err != nil {
		return routeEntry{outcome: routeDown}
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return routeEntry{outcome: routeDown}
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotFound:
		return routeEntry{outcome: routeUnknown}
	case http.StatusOK:
	default:
		return routeEntry{outcome: routeDown}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if err != nil || len(body) > 4096 {
		return routeEntry{outcome: routeDown}
	}
	var answer struct {
		HostPort int `json:"host_port"`
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if d.Decode(&answer) != nil || d.Decode(&struct{}{}) != io.EOF || answer.HostPort < 1 || answer.HostPort > 65535 {
		return routeEntry{outcome: routeDown}
	}
	return routeEntry{port: answer.HostPort}
}

// appPage writes a small self-contained page that says what happened in plain
// words and nothing internal. Titles and messages are fixed strings.
func appPage(w http.ResponseWriter, status int, title, message string) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>`+html.EscapeString(title)+`</title><style>body{font:16px/1.5 system-ui,sans-serif;margin:0;min-height:100vh;display:grid;place-items:center;background:#f6f6f4;color:#1c1c1a}main{max-width:30rem;padding:2rem}h1{font-size:1.25rem;margin:0 0 .5rem}p{margin:0;color:#55554f}@media(prefers-color-scheme:dark){body{background:#161615;color:#ececea}p{color:#a5a5a0}}</style></head><body><main><h1>`+html.EscapeString(title)+`</h1><p>`+html.EscapeString(message)+`</p></main></body></html>`)
}

// Server returns an http.Server for the application listener. There is no
// write timeout: it would end WebSocket and streaming responses.
func (p *AppProxy) Server(addr string) *http.Server {
	return &http.Server{Addr: addr, Handler: p, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 64 << 10}
}
