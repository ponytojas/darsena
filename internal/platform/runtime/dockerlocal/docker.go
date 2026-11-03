// Package dockerlocal implements an isolated, opt-in Docker Engine adapter.
// It is intentionally not connected to Dársena's demo processes.
package dockerlocal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ponytojas/darsena/internal/platform/configvault"
	rt "github.com/ponytojas/darsena/internal/platform/runtime"
	"github.com/ponytojas/darsena/internal/platform/runtime/configbridge"
)

const api = "/v1.47"
const maxResponse = 1 << 20
const memory = 256 * 1024 * 1024
const nanoCPU = 500000000
const pids = 64
const prefix = "io.darsena.runtime."

var containerID = regexp.MustCompile(`^[a-f0-9]{64}$`)
var _ rt.Runtime = (*Adapter)(nil)

// Adapter talks only to an explicitly configured local Unix socket. long has
// no client timeout and is used only for image pulls bounded by the caller.
type Adapter struct{ client, long *http.Client }

// New requires a private socket owned by the current non-root service account.
// Filesystem checks cannot prevent concurrent replacement; the enclosing directory
// must remain under the exclusive control of that account.
func New(socket string) (*Adapter, error) {
	if !filepath.IsAbs(socket) || filepath.Clean(socket) != socket || os.Geteuid() == 0 {
		return nil, rt.ErrInvalid
	}
	for p := socket; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return nil, rt.ErrInvalid
		}
		if p == socket {
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok || int(stat.Uid) != os.Geteuid() || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0077 != 0 {
				return nil, rt.ErrInvalid
			}
		} else {
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok || !info.IsDir() || (stat.Uid != 0 && int(stat.Uid) != os.Geteuid()) || info.Mode().Perm()&0022 != 0 {
				return nil, rt.ErrInvalid
			}
			if p == filepath.Dir(socket) && (int(stat.Uid) != os.Geteuid() || info.Mode().Perm()&0077 != 0) {
				return nil, rt.ErrInvalid
			}
		}
		if p == "/" {
			break
		}
	}
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, "unix", socket)
	}}
	return newAdapter(transport), nil
}
func newAdapter(transport http.RoundTripper) *Adapter {
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Adapter{
		client: &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: noRedirect},
		long:   &http.Client{Transport: transport, CheckRedirect: noRedirect},
	}
}

func contextError(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return rt.ErrTimeout
	}
	return rt.ErrCancelled
}
func (a *Adapter) request(ctx context.Context, method, path string, body any, mutate bool) (int, []byte, error) {
	if ctx.Err() != nil {
		return 0, nil, contextError(ctx)
	}
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil || len(data) > maxResponse {
			return 0, nil, rt.ErrInvalid
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, bytes.NewReader(data))
	if err != nil {
		return 0, nil, rt.ErrInvalid
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.client.Do(req)
	if err != nil {
		if mutate {
			return 0, nil, rt.ErrUnknownOutcome
		}
		if ctx.Err() != nil {
			return 0, nil, contextError(ctx)
		}
		return 0, nil, rt.ErrUnavailable
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil || len(b) > maxResponse {
		if mutate {
			return 0, nil, rt.ErrUnknownOutcome
		}
		return 0, nil, rt.ErrUnavailable
	}
	return resp.StatusCode, b, nil
}
func decode(b []byte, dest any) error {
	if json.Unmarshal(b, dest) != nil {
		return rt.ErrUnavailable
	}
	return nil
}

// Docker normally includes these fields even when they are null or false. Do
// not confuse missing security controls in a truncated response with defaults.
func fieldsPresent(b []byte, section string, required ...string) bool {
	var outer map[string]json.RawMessage
	if json.Unmarshal(b, &outer) != nil {
		return false
	}
	raw, ok := outer[section]
	if !ok || string(raw) == "null" {
		return false
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return false
	}
	for _, key := range required {
		if _, ok := obj[key]; !ok {
			return false
		}
	}
	return true
}

func fieldsNonNull(b []byte, section string, required ...string) bool {
	var outer map[string]json.RawMessage
	if json.Unmarshal(b, &outer) != nil {
		return false
	}
	obj := outer
	if section != "" {
		obj = nil
		if json.Unmarshal(outer[section], &obj) != nil {
			return false
		}
	}
	for _, key := range required {
		value, ok := obj[key]
		if !ok || string(value) == "null" {
			return false
		}
	}
	return true
}

func name(i rt.Identity) string {
	h := sha256.Sum256([]byte(i.Installation + "\x00" + i.Service + "\x00" + i.Operation))
	return "darsena-" + hex.EncodeToString(h[:])
}

// PrepareRef produces a stable reference before any request reaches the daemon.
func PrepareRef(s rt.Spec) (rt.Ref, error) {
	if s.Validate() != nil {
		return rt.Ref{}, rt.ErrInvalid
	}
	return rt.Ref{Identity: s.Identity, ResourceID: name(s.Identity), Fingerprint: fingerprint(s), Image: s.Image, Revision: s.Revision}, nil
}
func validRef(r rt.Ref) bool {
	s := rt.Spec{Identity: r.Identity, Image: r.Image, Revision: r.Revision}
	return r.Valid() && s.Validate() == nil && r.ResourceID == name(r.Identity) && r.Fingerprint == fingerprint(s)
}
func fingerprint(s rt.Spec) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("policy-v3:explicit-no-autoremove-no-publish-oom-enabled-private-cgroup:rootless:uid65532:readonly:capdrop-all:none-network:private-cgroup:no-swap:no-restart:no-log:seccomp-host\x00%s\x00%d\x00%d\x00%d\x00%d", s.Image, s.Revision, memory, nanoCPU, pids)))
	return hex.EncodeToString(h[:])
}
func labels(s rt.Spec) map[string]string {
	return map[string]string{prefix + "schema": "1", prefix + "installation": s.Identity.Installation, prefix + "service": s.Identity.Service, prefix + "operation": s.Identity.Operation, prefix + "fingerprint": fingerprint(s)}
}

// Check verifies the daemon's declared compatibility without examining an
// image or proving that kernel controls are effective.
func (a *Adapter) Check(ctx context.Context) error {
	status, b, err := a.request(ctx, http.MethodGet, "/version", nil, false)
	if err != nil {
		return err
	}
	var version struct {
		APIVersion    string
		MinAPIVersion string
	}
	if status != 200 || decode(b, &version) != nil || !apiCompatible(version.MinAPIVersion, version.APIVersion) {
		return rt.ErrUnavailable
	}
	status, b, err = a.request(ctx, http.MethodGet, api+"/info", nil, false)
	if err != nil {
		return err
	}
	var info struct {
		OSType          string
		SecurityOptions []string
		CgroupVersion   string
		CgroupDriver    string
		MemoryLimit     bool
		SwapLimit       bool
		CpuCfsQuota     bool
		PidsLimit       bool
	}
	if status != 200 || decode(b, &info) != nil {
		return rt.ErrUnavailable
	}
	rootless, seccomp := false, false
	for _, v := range info.SecurityOptions {
		if strings.HasPrefix(v, "name=rootless") {
			rootless = true
		}
		if strings.HasPrefix(v, "name=seccomp") {
			seccomp = true
		}
	}
	if info.OSType != "linux" || !rootless || !seccomp || info.CgroupVersion != "2" || info.CgroupDriver != "systemd" || !info.MemoryLimit || !info.SwapLimit || !info.CpuCfsQuota || !info.PidsLimit {
		return rt.ErrUnavailable
	}
	return nil
}
func (a *Adapter) preflight(ctx context.Context, image string) (string, error) {
	if err := a.Check(ctx); err != nil {
		return "", err
	}
	status, b, err := a.request(ctx, http.MethodGet, api+"/images/"+url.PathEscape(image)+"/json", nil, false)
	if err != nil {
		return "", err
	}
	if status == 404 {
		return "", rt.ErrNotFound
	}
	var img struct {
		ID     string `json:"Id"`
		Config struct{ Volumes map[string]json.RawMessage }
	}
	if status != 200 || !fieldsPresent(b, "Config", "Volumes") || decode(b, &img) != nil || !strings.HasPrefix(img.ID, "sha256:") || !containerID.MatchString(strings.TrimPrefix(img.ID, "sha256:")) || len(img.Config.Volumes) != 0 {
		return "", rt.ErrUnavailable
	}
	return img.ID, nil
}
func apiCompatible(min, max string) bool {
	parse := func(s string) (int, bool) {
		p := strings.Split(s, ".")
		if len(p) != 2 {
			return 0, false
		}
		a, e := strconv.Atoi(p[0])
		if e != nil {
			return 0, false
		}
		b, e := strconv.Atoi(p[1])
		return a*1000 + b, e == nil
	}
	lo, ok := parse(min)
	if !ok {
		return false
	}
	hi, ok := parse(max)
	return ok && lo <= 1047 && hi >= 1047
}

type observed struct {
	ID    string `json:"Id"`
	Image string
	State struct {
		Status  string
		Running bool
	}
	Config struct {
		User        string
		Env         []string
		Labels      map[string]string
		StopSignal  string
		StopTimeout *int
		Healthcheck struct{ Test []string }
	}
	HostConfig struct {
		Privileged        bool
		AutoRemove        bool
		PublishAllPorts   bool
		OomKillDisable    *bool
		Cgroup            string
		ReadonlyRootfs    bool
		CapDrop           []string
		CapAdd            []string
		SecurityOpt       []string
		NetworkMode       string
		PidMode           string
		IpcMode           string
		UTSMode           string
		UsernsMode        string
		CgroupnsMode      string
		CgroupParent      string
		Memory            int64
		MemorySwap        int64
		Runtime           string
		Sysctls           map[string]string
		GroupAdd          []string
		NanoCpus          int64
		PidsLimit         *int64
		Devices           []json.RawMessage
		DeviceRequests    []json.RawMessage
		DeviceCgroupRules []string
		Binds             []string
		Mounts            []json.RawMessage
		VolumesFrom       []string
		Tmpfs             map[string]string
		VolumeDriver      string
		PortBindings      map[string]json.RawMessage
		RestartPolicy     struct{ Name string }
		LogConfig         struct{ Type string }
	}
	Mounts []json.RawMessage
}

func (o observed) state() (rt.State, error) {
	switch o.State.Status {
	case "created":
		if !o.State.Running {
			return rt.StateCreated, nil
		}
	case "running":
		if o.State.Running {
			return rt.StateRunning, nil
		}
	case "exited", "dead":
		if !o.State.Running {
			return rt.StateStopped, nil
		}
	}
	return "", rt.ErrUnavailable
}
func contains(a []string, v string) bool {
	for _, x := range a {
		if x == v {
			return true
		}
	}
	return false
}
func (o observed) safe() bool {
	h := o.HostConfig
	return containerID.MatchString(o.ID) && o.Config.User == "65532:65532" && o.Config.StopSignal == "SIGTERM" && o.Config.StopTimeout != nil && *o.Config.StopTimeout == 10 && len(o.Config.Healthcheck.Test) == 1 && o.Config.Healthcheck.Test[0] == "NONE" && !h.Privileged && !h.AutoRemove && !h.PublishAllPorts && (h.OomKillDisable == nil || !*h.OomKillDisable) && h.Cgroup == "" && h.ReadonlyRootfs && len(h.CapDrop) == 1 && contains(h.CapDrop, "ALL") && len(h.CapAdd) == 0 && len(h.SecurityOpt) == 1 && contains(h.SecurityOpt, "no-new-privileges") && h.NetworkMode == "none" && (h.PidMode == "" || h.PidMode == "private") && (h.IpcMode == "" || h.IpcMode == "private") && h.UTSMode == "" && h.UsernsMode == "" && h.CgroupnsMode == "private" && h.CgroupParent == "" && h.Memory == memory && h.MemorySwap == memory && h.Runtime == "runc" && len(h.Sysctls) == 0 && len(h.GroupAdd) == 0 && h.NanoCpus == nanoCPU && h.PidsLimit != nil && *h.PidsLimit == pids && len(h.Devices) == 0 && len(h.DeviceRequests) == 0 && len(h.DeviceCgroupRules) == 0 && len(h.Binds) == 0 && len(h.Mounts) == 0 && len(h.VolumesFrom) == 0 && len(h.Tmpfs) == 0 && h.VolumeDriver == "" && len(h.PortBindings) == 0 && len(o.Mounts) == 0 && (h.RestartPolicy.Name == "" || h.RestartPolicy.Name == "no") && h.LogConfig.Type == "none"
}
func owned(o observed, i rt.Identity) bool {
	l := o.Config.Labels
	return l[prefix+"schema"] == "1" && l[prefix+"installation"] == i.Installation && l[prefix+"service"] == i.Service && l[prefix+"operation"] == i.Operation
}
func (a *Adapter) inspect(ctx context.Context, r rt.Ref) (observed, rt.Result, error) {
	var o observed
	if !validRef(r) {
		return o, rt.Result{}, rt.ErrInvalid
	}
	status, b, err := a.request(ctx, http.MethodGet, api+"/containers/"+r.ResourceID+"/json", nil, false)
	if err != nil {
		return o, rt.Result{}, err
	}
	if status == 404 {
		return o, rt.Result{}, rt.ErrNotFound
	}
	if status != 200 || !fieldsNonNull(b, "", "Id", "Image", "State", "Config", "HostConfig", "Mounts") || !fieldsNonNull(b, "State", "Status", "Running") || !fieldsNonNull(b, "Config", "User", "Labels", "Healthcheck", "StopSignal", "StopTimeout") || !fieldsPresent(b, "HostConfig", "Privileged", "ReadonlyRootfs", "CapDrop", "CapAdd", "SecurityOpt", "NetworkMode", "PidMode", "IpcMode", "UTSMode", "UsernsMode", "CgroupnsMode", "CgroupParent", "Memory", "MemorySwap", "Runtime", "AutoRemove", "PublishAllPorts", "OomKillDisable", "Cgroup", "GroupAdd", "NanoCpus", "PidsLimit", "Devices", "DeviceRequests", "DeviceCgroupRules", "Binds", "VolumesFrom", "VolumeDriver", "PortBindings", "RestartPolicy", "LogConfig") || !fieldsNonNull(b, "HostConfig", "Privileged", "ReadonlyRootfs", "NetworkMode", "PidMode", "IpcMode", "UTSMode", "UsernsMode", "CgroupnsMode", "CgroupParent", "Memory", "MemorySwap", "Runtime", "AutoRemove", "PublishAllPorts", "Cgroup", "NanoCpus", "PidsLimit", "VolumeDriver", "RestartPolicy", "LogConfig") || decode(b, &o) != nil {
		return o, rt.Result{}, rt.ErrUnavailable
	}
	if !owned(o, r.Identity) || o.Config.Labels[prefix+"fingerprint"] != r.Fingerprint {
		return o, rt.Result{}, rt.ErrConflict
	}
	if !o.safe() {
		return o, rt.Result{}, rt.ErrUnavailable
	}
	imageID, err := a.preflight(ctx, r.Image)
	if errors.Is(err, rt.ErrNotFound) {
		return o, rt.Result{}, rt.ErrUnavailable
	}
	if err != nil {
		return o, rt.Result{}, err
	}
	if o.Image != imageID {
		return o, rt.Result{}, rt.ErrConflict
	}
	state, err := o.state()
	if err != nil {
		return o, rt.Result{}, err
	}
	return o, rt.Result{Ref: r, State: state}, nil
}
func (a *Adapter) Inspect(ctx context.Context, r rt.Ref) (rt.Result, error) {
	_, res, err := a.inspect(ctx, r)
	return res, err
}

// CreateConfigured sends the accepted snapshot only in the Engine create body.
// Docker administrators can inspect container environments; callers must treat
// daemon access as access to these values.
func (a *Adapter) CreateConfigured(ctx context.Context, s rt.Spec, accepted configvault.Accepted) (rt.Result, error) {
	if accepted.Resource() != s.Identity.Service || accepted.Revision() <= 0 || uint64(accepted.Revision()) != s.Revision {
		return rt.Result{}, rt.ErrInvalid
	}
	env, err := configbridge.Environment(accepted)
	if err != nil {
		return rt.Result{}, rt.ErrInvalid
	}
	return a.create(ctx, s, env, true)
}

func (a *Adapter) Create(ctx context.Context, s rt.Spec) (rt.Result, error) {
	return a.create(ctx, s, nil, false)
}

// containsEnv compares every requested name and value without exposing them.
// Docker merges image defaults; additional inherited variables are allowed.
func containsEnv(actual, expected []string) bool {
	values := make(map[string]string, len(actual))
	for _, item := range actual {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			return false
		}
		if _, exists := values[key]; exists {
			return false
		}
		values[key] = value
	}
	for _, item := range expected {
		key, value, _ := strings.Cut(item, "=")
		if got, ok := values[key]; !ok || got != value {
			return false
		}
	}
	return true
}

func (a *Adapter) create(ctx context.Context, s rt.Spec, env []string, configured bool) (rt.Result, error) {
	if ctx.Err() != nil {
		return rt.Result{}, contextError(ctx)
	}
	if s.Validate() != nil {
		return rt.Result{}, rt.ErrInvalid
	}
	r, _ := PrepareRef(s)
	imageID, err := a.preflight(ctx, s.Image)
	if err != nil {
		return rt.Result{}, err
	}
	o, res, err := a.inspect(ctx, r)
	if err == nil {
		if o.Config.Labels[prefix+"fingerprint"] != fingerprint(s) || o.Image != imageID || (configured && !containsEnv(o.Config.Env, env)) {
			return rt.Result{}, rt.ErrConflict
		}
		return res, nil
	}
	if !errors.Is(err, rt.ErrNotFound) {
		return rt.Result{}, err
	}
	config := map[string]any{"Image": imageID, "User": "65532:65532", "StopSignal": "SIGTERM", "StopTimeout": 10, "Labels": labels(s), "Healthcheck": map[string]any{"Test": []string{"NONE"}}, "HostConfig": map[string]any{"Privileged": false, "AutoRemove": false, "PublishAllPorts": false, "OomKillDisable": false, "Cgroup": "", "ReadonlyRootfs": true, "CapDrop": []string{"ALL"}, "SecurityOpt": []string{"no-new-privileges"}, "NetworkMode": "none", "IpcMode": "private", "CgroupnsMode": "private", "Memory": memory, "MemorySwap": memory, "Runtime": "runc", "NanoCpus": nanoCPU, "PidsLimit": pids, "RestartPolicy": map[string]any{"Name": "no"}, "LogConfig": map[string]any{"Type": "none"}}}
	if configured {
		config["Env"] = env
	}
	status, b, err := a.request(ctx, http.MethodPost, api+"/containers/create?name="+url.QueryEscape(r.ResourceID), config, true)
	if err != nil {
		return rt.Result{Ref: r}, err
	}
	if status == 409 {
		o, res, err = a.inspect(ctx, r)
		if err != nil {
			return rt.Result{}, rt.ErrConflict
		}
		if o.Config.Labels[prefix+"fingerprint"] != fingerprint(s) || o.Image != imageID || (configured && !containsEnv(o.Config.Env, env)) {
			return rt.Result{}, rt.ErrConflict
		}
		return res, nil
	}
	if status != 201 {
		if status >= 500 {
			return rt.Result{Ref: r}, rt.ErrUnknownOutcome
		}
		return rt.Result{}, rt.ErrUnavailable
	}
	var created struct {
		ID string `json:"Id"`
	}
	if decode(b, &created) != nil || !containerID.MatchString(created.ID) {
		return rt.Result{Ref: r}, rt.ErrUnknownOutcome
	}
	o, res, err = a.inspect(ctx, r)
	if err != nil {
		return rt.Result{Ref: r, State: rt.StateCreated}, err
	}
	if o.ID != created.ID || o.Image != imageID || o.Config.Labels[prefix+"fingerprint"] != fingerprint(s) || res.State != rt.StateCreated || (configured && !containsEnv(o.Config.Env, env)) {
		return res, rt.ErrConflict
	}
	status, _, err = a.request(ctx, http.MethodPost, api+"/containers/"+o.ID+"/start", nil, true)
	if err != nil {
		return res, err
	}
	if status != 204 {
		if status >= 500 {
			return res, rt.ErrUnknownOutcome
		}
		return res, rt.ErrUnavailable
	}
	finalObserved, final, err := a.inspect(ctx, r)
	if err != nil || finalObserved.ID != o.ID || (configured && !containsEnv(finalObserved.Config.Env, env)) {
		return res, rt.ErrUnknownOutcome
	}
	return final, nil
}
func (a *Adapter) Stop(ctx context.Context, r rt.Ref) (rt.Result, error) {
	o, res, err := a.inspect(ctx, r)
	if err != nil {
		return rt.Result{}, err
	}
	if res.State != rt.StateRunning {
		return res, nil
	}
	status, _, err := a.request(ctx, http.MethodPost, api+"/containers/"+o.ID+"/stop?t=10", nil, true)
	if err != nil {
		return res, err
	}
	if status != 204 && status != 304 {
		if status >= 500 {
			return res, rt.ErrUnknownOutcome
		}
		return res, rt.ErrUnavailable
	}
	finalObserved, final, err := a.inspect(ctx, r)
	if err != nil || finalObserved.ID != o.ID {
		return res, rt.ErrUnknownOutcome
	}
	if final.State != rt.StateStopped {
		return res, rt.ErrUnknownOutcome
	}
	return final, nil
}
func (a *Adapter) Remove(ctx context.Context, r rt.Ref) error {
	o, res, err := a.inspect(ctx, r)
	if errors.Is(err, rt.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if res.State == rt.StateRunning {
		return rt.ErrConflict
	}
	status, _, err := a.request(ctx, http.MethodDelete, api+"/containers/"+o.ID, nil, true)
	if err != nil {
		return err
	}
	if status == 204 || status == 404 {
		return nil
	}
	if status == 409 {
		return rt.ErrConflict
	}
	if status >= 500 {
		return rt.ErrUnknownOutcome
	}
	return rt.ErrUnavailable
}
