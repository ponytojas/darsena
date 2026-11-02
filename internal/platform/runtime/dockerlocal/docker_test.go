package dockerlocal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	rt "github.com/ponytojas/darsena/internal/platform/runtime"
)

type engine struct {
	container          map[string]any
	image              string
	rootless           bool
	volumes            bool
	missingImageConfig bool
	unsafe             bool
	tamperEnv          bool
	loseCreate         bool
	loseStart          bool
	loseStop           bool
	loseRemove         bool
	failStart          bool
	posts              int
	startCalls         int
}

func (e *engine) RoundTrip(req *http.Request) (*http.Response, error) {
	response := func(code int, body any) (*http.Response, error) {
		b, _ := json.Marshal(body)
		return &http.Response{StatusCode: code, Body: io.NopCloser(bytes.NewReader(b)), Header: make(http.Header), Request: req}, nil
	}
	switch {
	case req.URL.Path == "/version":
		return response(200, map[string]any{"ApiVersion": "1.47", "MinAPIVersion": "1.24"})
	case req.URL.Path == api+"/info":
		security := []string{"name=seccomp,profile=default"}
		if e.rootless {
			security = append(security, "name=rootless")
		}
		return response(200, map[string]any{"OSType": "linux", "SecurityOptions": security, "CgroupVersion": "2", "CgroupDriver": "systemd", "MemoryLimit": true, "SwapLimit": true, "CpuCfsQuota": true, "PidsLimit": true})
	case strings.HasPrefix(req.URL.Path, api+"/images/"):
		if e.missingImageConfig {
			return response(200, map[string]any{"Id": e.image})
		}
		volumes := map[string]any{}
		if e.volumes {
			volumes["/data"] = map[string]any{}
		}
		return response(200, map[string]any{"Id": e.image, "Config": map[string]any{"Volumes": volumes}})
	case req.Method == "POST" && req.URL.Path == api+"/containers/create":
		e.posts++
		if e.container != nil {
			return response(409, nil)
		}
		var config map[string]any
		if json.NewDecoder(req.Body).Decode(&config) != nil {
			panic("bad create")
		}
		host := config["HostConfig"].(map[string]any)
		delete(config, "HostConfig")
		for _, key := range []string{"CapAdd", "Devices", "DeviceRequests", "DeviceCgroupRules", "Binds", "Mounts", "VolumesFrom", "Tmpfs", "PortBindings", "Sysctls", "GroupAdd"} {
			host[key] = nil
		}
		for _, key := range []string{"PidMode", "UTSMode", "UsernsMode", "CgroupParent", "VolumeDriver"} {
			host[key] = ""
		}
		if e.unsafe {
			host["Privileged"] = true
		}
		if e.tamperEnv {
			config["Env"] = []string{"TOKEN=wrong"}
		}
		e.container = map[string]any{"Id": strings.Repeat("a", 64), "Image": e.image, "Config": config, "HostConfig": host, "State": map[string]any{"Status": "created", "Running": false}, "Mounts": []any{}}
		if e.loseCreate {
			return nil, errors.New("SENSITIVE daemon error")
		}
		return response(201, map[string]any{"Id": strings.Repeat("a", 64)})
	case req.Method == "GET" && strings.Contains(req.URL.Path, "/containers/"):
		if e.container == nil {
			return response(404, map[string]any{"message": "SENSITIVE"})
		}
		return response(200, e.container)
	case req.Method == "POST" && strings.HasSuffix(req.URL.Path, "/start"):
		e.startCalls++
		if e.failStart {
			return response(400, map[string]any{"message": "SENSITIVE"})
		}
		e.container["State"] = map[string]any{"Status": "running", "Running": true}
		if e.loseStart {
			return nil, errors.New("SENSITIVE")
		}
		return response(204, nil)
	case req.Method == "POST" && strings.HasSuffix(req.URL.Path, "/stop"):
		e.container["State"] = map[string]any{"Status": "exited", "Running": false}
		if e.loseStop {
			return nil, errors.New("SENSITIVE")
		}
		return response(204, nil)
	case req.Method == "DELETE":
		e.container = nil
		if e.loseRemove {
			return nil, errors.New("SENSITIVE")
		}
		return response(204, nil)
	}
	panic(fmt.Sprintf("unexpected request %s %s", req.Method, req.URL.Path))
}
func spec() rt.Spec {
	return rt.Spec{Identity: rt.Identity{Installation: "install-1", Service: "service-1", Operation: "operation-1"}, Image: "registry.example/app@sha256:" + strings.Repeat("b", 64), Revision: 1}
}
func fake() *engine { return &engine{rootless: true, image: "sha256:" + strings.Repeat("c", 64)} }
func TestLifecycleAndConflict(t *testing.T) {
	e := fake()
	a := newAdapter(e)
	ctx := context.Background()
	s := spec()
	got, err := a.Create(ctx, s)
	if err != nil || got.State != rt.StateRunning {
		t.Fatalf("create %v %v", got, err)
	}
	again, err := a.Create(ctx, s)
	if err != nil || again != got || e.posts != 1 {
		t.Fatalf("repeat %v %v %d", again, err, e.posts)
	}
	changed := s
	changed.Revision++
	if _, err = a.Create(ctx, changed); !errors.Is(err, rt.ErrConflict) {
		t.Fatalf("conflict: %v", err)
	}
	if err = a.Remove(ctx, got.Ref); !errors.Is(err, rt.ErrConflict) {
		t.Fatalf("remove running %v", err)
	}
	stopped, err := a.Stop(ctx, got.Ref)
	if err != nil || stopped.State != rt.StateStopped {
		t.Fatalf("stop %v %v", stopped, err)
	}
	if _, err = a.Stop(ctx, got.Ref); err != nil {
		t.Fatal(err)
	}
	if err = a.Remove(ctx, got.Ref); err != nil {
		t.Fatal(err)
	}
	if err = a.Remove(ctx, got.Ref); err != nil {
		t.Fatal(err)
	}
}
func TestFailClosedBeforeStart(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*engine)
		want   error
		post   bool
	}{
		{"no rootless", func(e *engine) { e.rootless = false }, rt.ErrUnavailable, false},
		{"image volume", func(e *engine) { e.volumes = true }, rt.ErrUnavailable, false},
		{"missing image configuration", func(e *engine) { e.missingImageConfig = true }, rt.ErrUnavailable, false},
		{"unsafe effective configuration", func(e *engine) { e.unsafe = true }, rt.ErrUnavailable, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := fake()
			tc.mutate(e)
			a := newAdapter(e)
			res, err := a.Create(context.Background(), spec())
			if !errors.Is(err, tc.want) {
				t.Fatalf("%v", err)
			}
			if e.startCalls != 0 || (e.posts > 0) != tc.post {
				t.Fatalf("posts %d starts %d", e.posts, e.startCalls)
			}
			if tc.post && !res.Ref.Valid() {
				t.Fatal("missing partial reference")
			}
		})
	}
}
func TestUnknownOutcomeAndOwnership(t *testing.T) {
	e := fake()
	e.loseCreate = true
	a := newAdapter(e)
	s := spec()
	res, err := a.Create(context.Background(), s)
	if !errors.Is(err, rt.ErrUnknownOutcome) || !res.Ref.Valid() || strings.Contains(err.Error(), "SENSITIVE") {
		t.Fatalf("%v %v", res, err)
	}
	got, err := a.Inspect(context.Background(), res.Ref)
	if err != nil || got.State != rt.StateCreated {
		t.Fatalf("reconcile %v %v", got, err)
	}
	e.container["Config"].(map[string]any)["Labels"].(map[string]any)[prefix+"installation"] = "foreign"
	if _, err = a.Inspect(context.Background(), res.Ref); !errors.Is(err, rt.ErrConflict) {
		t.Fatalf("foreign %v", err)
	}
	if err = a.Remove(context.Background(), res.Ref); !errors.Is(err, rt.ErrConflict) || e.container == nil {
		t.Fatalf("foreign removal %v", err)
	}
}

func TestRejectsIncompleteAndUnsafeInspect(t *testing.T) {
	for _, tc := range []struct {
		name  string
		alter func(map[string]any)
	}{
		{"missing privileged", func(h map[string]any) { delete(h, "Privileged") }},
		{"null privileged", func(h map[string]any) { h["Privileged"] = nil }},
		{"host cgroup namespace", func(h map[string]any) { h["CgroupnsMode"] = "host" }},
		{"default cgroup namespace", func(h map[string]any) { h["CgroupnsMode"] = "" }},
		{"device requests", func(h map[string]any) { h["DeviceRequests"] = []any{map[string]any{"Count": 1}} }},
		{"device rules", func(h map[string]any) { h["DeviceCgroupRules"] = []string{"a *:* rwm"} }},
		{"volumes from", func(h map[string]any) { h["VolumesFrom"] = []string{"foreign"} }},
		{"tmpfs", func(h map[string]any) { h["Tmpfs"] = map[string]string{"/tmp": "rw"} }},
		{"mounts", func(h map[string]any) {
			h["Mounts"] = []any{map[string]any{"Type": "bind", "Source": "/host", "Target": "/data"}}
		}},
		{"volume driver", func(h map[string]any) { h["VolumeDriver"] = "custom" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := fake()
			a := newAdapter(e)
			res, err := a.Create(context.Background(), spec())
			if err != nil {
				t.Fatal(err)
			}
			tc.alter(e.container["HostConfig"].(map[string]any))
			if _, err := a.Stop(context.Background(), res.Ref); !errors.Is(err, rt.ErrUnavailable) {
				t.Fatalf("unsafe inspect: %v", err)
			}
			if e.container["State"].(map[string]any)["Running"] != true {
				t.Fatal("unsafe resource was mutated")
			}
		})
	}
}
func TestCancelledAndInvalidSocket(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e := fake()
	_, err := newAdapter(e).Create(ctx, spec())
	if !errors.Is(err, rt.ErrCancelled) || e.posts != 0 {
		t.Fatal(err)
	}
	for _, path := range []string{"", "relative", "/definitely/missing/socket"} {
		if _, err = New(path); !errors.Is(err, rt.ErrInvalid) {
			t.Fatalf("socket: %v", err)
		}
	}
}

type brokenTransport struct{ body string }

func (b brokenTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(b.body)), Request: r}, nil
}
func TestMalformedAndOversizedAreSanitized(t *testing.T) {
	for _, body := range []string{"SENSITIVE", strings.Repeat("SENSITIVE", maxResponse)} {
		_, err := newAdapter(brokenTransport{body}).Create(context.Background(), spec())
		if !errors.Is(err, rt.ErrUnavailable) || strings.Contains(err.Error(), "SENSITIVE") {
			t.Fatal(err)
		}
	}
}

func TestPrivateUnixSocket(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the adapter intentionally rejects a root service account")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(home, ".ds-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "engine.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	e := fake()
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp, err := e.RoundTrip(r)
		if err != nil {
			w.WriteHeader(500)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	})}
	defer server.Close()
	go func() { _ = server.Serve(listener) }()
	a, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Create(context.Background(), spec()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0666); err != nil {
		t.Fatal(err)
	}
	if _, err := New(path); !errors.Is(err, rt.ErrInvalid) {
		t.Fatalf("public socket: %v", err)
	}
	link := filepath.Join(dir, "link.sock")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := New(link); !errors.Is(err, rt.ErrInvalid) {
		t.Fatalf("symlink: %v", err)
	}
	// An otherwise private socket directory must not be nested below a
	// directory replaceable by another account.
	inner := filepath.Join(dir, "inner")
	if err := os.Mkdir(inner, 0700); err != nil {
		t.Fatal(err)
	}
	innerSocket := filepath.Join(inner, "s")
	innerListener, err := net.Listen("unix", innerSocket)
	if err != nil {
		t.Fatal(err)
	}
	defer innerListener.Close()
	if err := os.Chmod(innerSocket, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0777); err != nil {
		t.Fatal(err)
	}
	if _, err := New(innerSocket); !errors.Is(err, rt.ErrInvalid) {
		t.Fatalf("writable ancestor: %v", err)
	}
}

func TestFingerprintBindsAllOperations(t *testing.T) {
	e := fake()
	a := newAdapter(e)
	result, err := a.Create(context.Background(), spec())
	if err != nil {
		t.Fatal(err)
	}
	different := spec()
	different.Revision++
	other, err := PrepareRef(different)
	if err != nil || other.ResourceID != result.Ref.ResourceID || other.Fingerprint == result.Ref.Fingerprint {
		t.Fatal("bad prepared reference")
	}
	if _, err := a.Inspect(context.Background(), other); !errors.Is(err, rt.ErrConflict) {
		t.Fatalf("inspect: %v", err)
	}
	if _, err := a.Stop(context.Background(), other); !errors.Is(err, rt.ErrConflict) {
		t.Fatalf("stop: %v", err)
	}
	if err := a.Remove(context.Background(), other); !errors.Is(err, rt.ErrConflict) {
		t.Fatalf("remove: %v", err)
	}
	if e.container["State"].(map[string]any)["Running"] != true {
		t.Fatal("mutated mismatched resource")
	}
}

func TestRejectsUnsafeAdditionalControls(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"swap", func(h map[string]any) { h["MemorySwap"] = -1 }},
		{"runtime", func(h map[string]any) { h["Runtime"] = "other" }},
		{"default runtime", func(h map[string]any) { h["Runtime"] = "" }},
		{"sysctls", func(h map[string]any) { h["Sysctls"] = map[string]string{"net.ipv4.ip_forward": "1"} }},
		{"groups", func(h map[string]any) { h["GroupAdd"] = []string{"0"} }},
		{"missing swap", func(h map[string]any) { delete(h, "MemorySwap") }},
		{"auto remove", func(h map[string]any) { h["AutoRemove"] = true }},
		{"auto publish", func(h map[string]any) { h["PublishAllPorts"] = true }},
		{"oom killer disabled", func(h map[string]any) { h["OomKillDisable"] = true }},
		{"foreign cgroup", func(h map[string]any) { h["Cgroup"] = "container:foreign" }},
		{"missing auto remove", func(h map[string]any) { delete(h, "AutoRemove") }},
		{"missing auto publish", func(h map[string]any) { delete(h, "PublishAllPorts") }},
		{"missing oom policy", func(h map[string]any) { delete(h, "OomKillDisable") }},
		{"missing cgroup", func(h map[string]any) { delete(h, "Cgroup") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := fake()
			a := newAdapter(e)
			result, err := a.Create(context.Background(), spec())
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(e.container["HostConfig"].(map[string]any))
			if _, err = a.Stop(context.Background(), result.Ref); !errors.Is(err, rt.ErrUnavailable) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestContradictoryStateRejected(t *testing.T) {
	e := fake()
	a := newAdapter(e)
	result, err := a.Create(context.Background(), spec())
	if err != nil {
		t.Fatal(err)
	}
	e.container["State"] = map[string]any{"Status": "created", "Running": true}
	if _, err = a.Inspect(context.Background(), result.Ref); !errors.Is(err, rt.ErrUnavailable) {
		t.Fatal(err)
	}
}

type replacementTransport struct {
	engine    *engine
	replaceOn string
}

func (r replacementTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := r.engine.RoundTrip(req)
	if err == nil && strings.HasSuffix(req.URL.Path, r.replaceOn) {
		r.engine.container["Id"] = strings.Repeat("d", 64)
	}
	return resp, err
}
func TestReplacementAfterMutationIsUnknown(t *testing.T) {
	for _, operation := range []string{"start", "stop"} {
		t.Run(operation, func(t *testing.T) {
			e := fake()
			a := newAdapter(e)
			if operation == "start" {
				a = newAdapter(replacementTransport{e, "/start"})
				_, err := a.Create(context.Background(), spec())
				if !errors.Is(err, rt.ErrUnknownOutcome) {
					t.Fatal(err)
				}
				return
			}
			result, err := a.Create(context.Background(), spec())
			if err != nil {
				t.Fatal(err)
			}
			a = newAdapter(replacementTransport{e, "/stop"})
			_, err = a.Stop(context.Background(), result.Ref)
			if !errors.Is(err, rt.ErrUnknownOutcome) {
				t.Fatal(err)
			}
		})
	}
}

func TestLostMutationResponsesAndPartialStart(t *testing.T) {
	ctx := context.Background()
	t.Run("lost start", func(t *testing.T) {
		e := fake()
		e.loseStart = true
		a := newAdapter(e)
		result, err := a.Create(ctx, spec())
		if !errors.Is(err, rt.ErrUnknownOutcome) || strings.Contains(err.Error(), "SENSITIVE") {
			t.Fatal(err)
		}
		observed, err := a.Inspect(ctx, result.Ref)
		if err != nil || observed.State != rt.StateRunning {
			t.Fatalf("%v %v", observed, err)
		}
		_, err = a.Create(ctx, spec())
		if err != nil || e.startCalls != 1 {
			t.Fatalf("retry: %v starts %d", err, e.startCalls)
		}
	})
	t.Run("failed start", func(t *testing.T) {
		e := fake()
		e.failStart = true
		a := newAdapter(e)
		result, err := a.Create(ctx, spec())
		if !errors.Is(err, rt.ErrUnavailable) || result.State != rt.StateCreated {
			t.Fatalf("%v %v", result, err)
		}
		_, err = a.Create(ctx, spec())
		if err != nil || e.startCalls != 1 {
			t.Fatalf("retry: %v starts %d", err, e.startCalls)
		}
	})
	t.Run("lost stop and remove", func(t *testing.T) {
		e := fake()
		a := newAdapter(e)
		result, err := a.Create(ctx, spec())
		if err != nil {
			t.Fatal(err)
		}
		e.loseStop = true
		_, err = a.Stop(ctx, result.Ref)
		if !errors.Is(err, rt.ErrUnknownOutcome) {
			t.Fatal(err)
		}
		observed, err := a.Inspect(ctx, result.Ref)
		if err != nil || observed.State != rt.StateStopped {
			t.Fatalf("%v %v", observed, err)
		}
		e.loseRemove = true
		err = a.Remove(ctx, result.Ref)
		if !errors.Is(err, rt.ErrUnknownOutcome) {
			t.Fatal(err)
		}
		if _, err = a.Inspect(ctx, result.Ref); !errors.Is(err, rt.ErrNotFound) {
			t.Fatal(err)
		}
	})
}

func TestImageAndReferenceCannotBeSubstituted(t *testing.T) {
	e := fake()
	a := newAdapter(e)
	ctx := context.Background()
	result, err := a.Create(ctx, spec())
	if err != nil {
		t.Fatal(err)
	}
	e.container["Image"] = "sha256:" + strings.Repeat("d", 64)
	if _, err = a.Inspect(ctx, result.Ref); !errors.Is(err, rt.ErrConflict) {
		t.Fatalf("wrong image: %v", err)
	}
	if _, err = a.Stop(ctx, result.Ref); !errors.Is(err, rt.ErrConflict) {
		t.Fatalf("wrong image stop: %v", err)
	}
	altered := result.Ref
	altered.Image = "registry.example/other@sha256:" + strings.Repeat("d", 64)
	if _, err = a.Inspect(ctx, altered); !errors.Is(err, rt.ErrInvalid) {
		t.Fatalf("altered ref: %v", err)
	}
}

// Moby v27.5.1's HostConfig marks Tmpfs, Sysctls and Mounts omitempty.
// This static Engine API v1.47-shaped response intentionally omits them; it is
// not generated from the adapter's create request or observed Go struct.
func TestInspectEngineV147OptionalCollections(t *testing.T) {
	b, err := os.ReadFile("testdata/inspect-engine-v147.json")
	if err != nil {
		t.Fatal(err)
	}
	e := fake()
	if err := json.Unmarshal(b, &e.container); err != nil {
		t.Fatal(err)
	}
	e.container["Config"].(map[string]any)["Labels"] = labels(spec())
	r, err := PrepareRef(spec())
	if err != nil {
		t.Fatal(err)
	}
	got, err := newAdapter(e).Inspect(context.Background(), r)
	if err != nil || got.State != rt.StateRunning {
		t.Fatalf("inspect optional fields: %v %v", got.State, err)
	}
}

func TestPreviousPolicyReferenceRejected(t *testing.T) {
	s := spec()
	r, err := PrepareRef(s)
	if err != nil {
		t.Fatal(err)
	}
	old := sha256.Sum256([]byte(fmt.Sprintf("policy-v2:rootless:uid65532:readonly:capdrop-all:none-network:private-cgroup:no-swap:no-restart:no-log:seccomp-host\x00%s\x00%d\x00%d\x00%d\x00%d", s.Image, s.Revision, memory, nanoCPU, pids)))
	r.Fingerprint = fmt.Sprintf("%x", old)
	e := fake()
	if _, err := newAdapter(e).Inspect(context.Background(), r); !errors.Is(err, rt.ErrInvalid) {
		t.Fatalf("old policy: %v", err)
	}
	if e.posts != 0 {
		t.Fatal("old policy caused mutation")
	}
}
