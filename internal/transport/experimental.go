package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"time"
)

// experimentalWorkerMatches fails closed when the worker identity or policy
// cannot be established. It never trusts headers from the browser.
func (a API) experimentalWorkerMatches(parent context.Context) bool {
	if a.Owner == nil || a.WorkerSocket == "" || a.Installation == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	t := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", a.WorkerSocket)
	}}
	defer t.CloseIdleConnections()
	c := &http.Client{Transport: t, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://worker/runtime/info", nil)
	if err != nil {
		return false
	}
	resp, err := c.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if err != nil || len(body) > 4096 {
		return false
	}
	var info struct {
		Mode         string `json:"mode"`
		Installation string `json:"installation"`
		Policy       string `json:"policy"`
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	return d.Decode(&info) == nil && d.Decode(&struct{}{}) == io.EOF && info.Mode == "experimental" && info.Installation == a.Installation && info.Policy == "dockerlocal-v3"
}

// experimentalRuntimeReady checks the worker's bounded, read-only daemon
// preflight. It does not establish image availability or kernel isolation.
func (a API) experimentalRuntimeReady(parent context.Context) bool {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	t := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", a.WorkerSocket)
	}}
	defer t.CloseIdleConnections()
	c := &http.Client{Transport: t, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://worker/runtime/ready", nil)
	if err != nil {
		return false
	}
	resp, err := c.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if err != nil || len(body) > 4096 {
		return false
	}
	var status struct {
		Status string `json:"status"`
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	return d.Decode(&status) == nil && d.Decode(&struct{}{}) == io.EOF && status.Status == "ready"
}
