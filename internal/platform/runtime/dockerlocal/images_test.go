package dockerlocal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	rt "github.com/ponytojas/darsena/internal/platform/runtime"
)

type imageEngine struct {
	present   bool
	os, arch  string
	volumes   bool
	pullLines []string
	pullCode  int
	pulled    bool
	query     string
	auth      bool
	resolve   int
	platforms []map[string]string
}

func (e *imageEngine) RoundTrip(req *http.Request) (*http.Response, error) {
	reply := func(code int, body any) (*http.Response, error) {
		b, _ := json.Marshal(body)
		return &http.Response{StatusCode: code, Body: io.NopCloser(bytes.NewReader(b)), Header: make(http.Header), Request: req}, nil
	}
	if req.Header.Get("X-Registry-Auth") != "" {
		e.auth = true
	}
	switch {
	case req.URL.Path == "/version":
		return reply(200, map[string]any{"Os": "linux", "Arch": "arm64"})
	case req.Method == "GET" && strings.HasPrefix(req.URL.Path, api+"/images/"):
		if !e.present {
			return reply(404, map[string]any{"message": "SENSITIVE"})
		}
		volumes := map[string]any{}
		if e.volumes {
			volumes["/data"] = map[string]any{}
		}
		return reply(200, map[string]any{"Id": "sha256:" + strings.Repeat("c", 64), "Os": e.os, "Architecture": e.arch, "Config": map[string]any{"Volumes": volumes}})
	case req.Method == "GET" && strings.HasPrefix(req.URL.Path, api+"/distribution/"):
		if e.resolve != 200 {
			return reply(e.resolve, map[string]any{"message": "SENSITIVE"})
		}
		return reply(200, map[string]any{"Descriptor": map[string]any{"digest": "sha256:" + strings.Repeat("d", 64)}, "Platforms": e.platforms})
	case req.Method == "POST" && req.URL.Path == api+"/images/create":
		e.query = req.URL.RawQuery
		code := e.pullCode
		if code == 0 {
			code = 200
		}
		if e.pulled {
			e.present = true
		}
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(strings.Join(e.pullLines, "\n"))), Header: make(http.Header), Request: req}, nil
	}
	return reply(500, nil)
}

var pinned = "docker.io/library/hello-world@sha256:" + strings.Repeat("a", 64)

func TestImageStatus(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		e      imageEngine
		state  rt.ImageState
		reason string
	}{
		{imageEngine{}, rt.ImageMissing, ""},
		{imageEngine{present: true, os: "linux", arch: "arm64"}, rt.ImageAvailable, ""},
		{imageEngine{present: true, os: "windows", arch: "amd64"}, rt.ImageIncompatible, "platform"},
		{imageEngine{present: true, os: "linux", arch: "arm64", volumes: true}, rt.ImageIncompatible, "volumes"},
	}
	for _, c := range cases {
		got, err := newAdapter(&c.e).ImageStatus(ctx, pinned)
		if err != nil || got.State != c.state || got.Reason != c.reason || got.DaemonPlatform != "linux/arm64" {
			t.Errorf("%+v: got %+v %v", c.e, got, err)
		}
	}
	if _, err := newAdapter(&imageEngine{}).ImageStatus(ctx, "hello-world@sha256:"+strings.Repeat("a", 64)); !errors.Is(err, rt.ErrInvalid) {
		t.Fatal("non-canonical reference accepted")
	}
}

func TestResolveTag(t *testing.T) {
	ctx := context.Background()
	digest, err := newAdapter(&imageEngine{resolve: 200}).ResolveTag(ctx, "docker.io/library/nginx:1.27")
	if err != nil || digest != "sha256:"+strings.Repeat("d", 64) {
		t.Fatalf("resolve %q %v", digest, err)
	}
	if _, err := newAdapter(&imageEngine{resolve: 401}).ResolveTag(ctx, "docker.io/library/private:1"); !errors.Is(err, rt.ErrNotFound) {
		t.Fatalf("private image: %v", err)
	}
	if _, err := newAdapter(&imageEngine{resolve: 200}).ResolveTag(ctx, "nginx:1.27"); !errors.Is(err, rt.ErrInvalid) {
		t.Fatal("non-canonical tag accepted")
	}
}

func TestRegistrySupports(t *testing.T) {
	ctx := context.Background()
	windows := []map[string]string{{"os": "windows", "architecture": "amd64"}, {"os": "unknown", "architecture": "unknown"}}
	if ok, platform, err := newAdapter(&imageEngine{resolve: 200, platforms: windows}).RegistrySupports(ctx, pinned); err != nil || ok || platform != "windows/amd64" {
		t.Fatalf("windows only: %v %q %v", ok, platform, err)
	}
	multi := append(windows, map[string]string{"os": "linux", "architecture": "arm64"})
	if ok, _, err := newAdapter(&imageEngine{resolve: 200, platforms: multi}).RegistrySupports(ctx, pinned); err != nil || !ok {
		t.Fatalf("multi-platform: %v %v", ok, err)
	}
	if ok, _, err := newAdapter(&imageEngine{resolve: 200}).RegistrySupports(ctx, pinned); err != nil || !ok {
		t.Fatalf("no platform data: %v %v", ok, err)
	}
	if _, _, err := newAdapter(&imageEngine{resolve: 404}).RegistrySupports(ctx, pinned); !errors.Is(err, rt.ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
}

func TestPull(t *testing.T) {
	ctx := context.Background()
	e := &imageEngine{os: "linux", arch: "arm64", pulled: true, pullLines: []string{`{"status":"Pulling"}`, `{"status":"Downloaded"}`}}
	if err := newAdapter(e).Pull(ctx, pinned); err != nil {
		t.Fatalf("pull: %v", err)
	}
	if e.auth || !strings.Contains(e.query, "fromImage=docker.io%2Flibrary%2Fhello-world") || !strings.Contains(e.query, "tag=sha256%3A") {
		t.Fatalf("request: auth=%v query=%s", e.auth, e.query)
	}
	failures := []struct {
		line string
		want error
	}{
		{`{"error":"no matching manifest for linux/arm64 SENSITIVE"}`, rt.ErrImagePlatform},
		{`{"error":"manifest unknown SENSITIVE"}`, rt.ErrNotFound},
		{`{"error":"pull access denied SENSITIVE"}`, rt.ErrNotFound},
		{`{"error":"disk full SENSITIVE"}`, rt.ErrUnavailable},
	}
	for _, f := range failures {
		err := newAdapter(&imageEngine{pullLines: []string{`{"status":"x"}`, f.line}}).Pull(ctx, pinned)
		if !errors.Is(err, f.want) || strings.Contains(err.Error(), "SENSITIVE") {
			t.Errorf("%s: %v", f.line, err)
		}
	}
	if err := newAdapter(&imageEngine{pullCode: 404}).Pull(ctx, pinned); !errors.Is(err, rt.ErrNotFound) {
		t.Fatalf("404: %v", err)
	}
	// A stream without errors that leaves no image is not a success.
	if err := newAdapter(&imageEngine{pullLines: []string{`{"status":"ok"}`}}).Pull(ctx, pinned); !errors.Is(err, rt.ErrUnavailable) {
		t.Fatalf("silent failure: %v", err)
	}
}

func TestInspectReportsExitCodeOnlyWhenStopped(t *testing.T) {
	ctx := context.Background()
	e := fake()
	a := newAdapter(e)
	got, err := a.Create(ctx, spec())
	if err != nil || got.ExitCode != nil {
		t.Fatalf("running: %+v %v", got, err)
	}
	e.container["State"] = map[string]any{"Status": "exited", "Running": false, "ExitCode": 3}
	got, err = a.Inspect(ctx, got.Ref)
	if err != nil || got.ExitCode == nil || *got.ExitCode != 3 {
		t.Fatalf("exited: %+v %v", got, err)
	}
}
