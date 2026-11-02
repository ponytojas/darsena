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

type capabilitiesResponse struct {
	Mode          string `json:"mode"`
	RealExecution struct {
		Enabled       bool   `json:"enabled"`
		Provider      string `json:"provider"`
		Configuration string `json:"configuration"`
	} `json:"real_execution"`
	Worker struct {
		Configured bool   `json:"configured"`
		Status     string `json:"status"`
	} `json:"worker"`
	State string `json:"state"`
	Vault string `json:"vault"`
}

type workerReadiness struct {
	Mode  string `json:"mode"`
	State string `json:"state"`
	Vault string `json:"vault"`
}

// capabilities only reports fixed categories. A broken or untrusted worker response
// cannot supply arbitrary strings to the public API.
func (a API) capabilities(w http.ResponseWriter, r *http.Request) {
	out := capabilitiesResponse{Mode: "demo", State: "disabled", Vault: "disabled"}
	out.RealExecution.Provider = "disabled"
	out.RealExecution.Configuration = "not_applied"
	out.Worker.Status = "disabled"
	if a.Owner != nil {
		out.Mode = "experimental"
		out.RealExecution.Enabled = true
		out.RealExecution.Provider = "unavailable"
		out.RealExecution.Configuration = "accepted_revision"
	}
	if a.WorkerSocket != "" {
		out.Worker.Configured = true
		out.Worker.Status = "unavailable"
		out.State, out.Vault = "unknown", "unknown"
		expectedMode := "simulation"
		if a.Owner != nil {
			expectedMode = "experimental"
		}
		if ready, ok := queryWorkerReadinessMode(r.Context(), a.WorkerSocket, expectedMode); ok && (a.Owner == nil || a.experimentalWorkerMatches(r.Context())) {
			out.State, out.Vault = ready.State, ready.Vault
			if ready.State == "ready" && ready.Vault != "unavailable" {
				out.Worker.Status = "ready"
				if a.Owner != nil && ready.Vault == "ready" && a.experimentalRuntimeReady(r.Context()) {
					out.RealExecution.Provider = "ready"
				}
			}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	send(w, http.StatusOK, out)
}

func queryWorkerReadiness(parent context.Context, socket string) (workerReadiness, bool) {
	return queryWorkerReadinessMode(parent, socket, "simulation")
}

func queryWorkerReadinessMode(parent context.Context, socket, mode string) (workerReadiness, bool) {
	var result workerReadiness
	ctx, cancel := context.WithTimeout(parent, 1500*time.Millisecond)
	defer cancel()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://worker/readyz", nil)
	if err != nil {
		return result, false
	}
	resp, err := client.Do(req)
	if err != nil {
		return result, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusServiceUnavailable {
		return result, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if err != nil || len(body) > 4096 {
		return result, false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return workerReadiness{}, false
	}
	if result.Mode != mode || (result.State != "ready" && result.State != "unavailable") || (result.Vault != "ready" && result.Vault != "disabled" && result.Vault != "unavailable") {
		return workerReadiness{}, false
	}
	ready := result.State == "ready" && result.Vault != "unavailable"
	if ready != (resp.StatusCode == http.StatusOK) {
		return workerReadiness{}, false
	}
	return result, true
}
