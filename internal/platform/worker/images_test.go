package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/ponytojas/darsena/internal/platform/configvault"
	rt "github.com/ponytojas/darsena/internal/platform/runtime"
	"github.com/ponytojas/darsena/internal/platform/runtime/dockerlocal"
	"github.com/ponytojas/darsena/internal/platform/runtime/intent"
)

type imageHarness struct {
	t        *testing.T
	s        *Service
	vault    *configvault.Vault
	provider *testProvider
}

func newImageHarness(t *testing.T, runtime bool) *imageHarness {
	t.Helper()
	ctx := context.Background()
	// Unix socket paths are short; t.TempDir embeds the long test name.
	dir, err := os.MkdirTemp("", "wk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, filepath.Join(dir, "state.db"), filepath.Join(dir, "socket"))
	if err != nil {
		t.Fatal(err)
	}
	h := &imageHarness{t: t, s: s, provider: &testProvider{}}
	t.Cleanup(func() { _ = s.Close() })
	if !runtime {
		return h
	}
	key, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	if h.vault, err = configvault.Open(ctx, filepath.Join(dir, "vault.db"), key); err != nil {
		t.Fatal(err)
	}
	if err = s.EnableConfig(h.vault); err != nil {
		t.Fatal(err)
	}
	store, err := intent.Open(ctx, filepath.Join(dir, "intent.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.EnableRuntime("install", store, dockerlocal.PrepareRef, h.provider); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *imageHarness) do(method, path, body string) (int, map[string]string) {
	h.t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.s.mux.ServeHTTP(rec, req)
	out := map[string]string{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

var hexA = strings.Repeat("a", 64)

func TestCheckImageWithoutRuntimeOnlyNormalizes(t *testing.T) {
	h := newImageHarness(t, false)
	code, body := h.do("GET", "/image?ref=hello-world:nanoserver@sha256:"+hexA, "")
	if code != 200 || body["reference"] != "docker.io/library/hello-world@sha256:"+hexA || body["status"] != "not_checked" {
		t.Fatalf("%d %v", code, body)
	}
	if code, body = h.do("GET", "/image?ref=nginx:1.27", ""); code != 400 || body["code"] != "image_digest_required" {
		t.Fatalf("tag without runtime: %d %v", code, body)
	}
	if code, body = h.do("GET", "/image?ref=Not%20valid", ""); code != 400 || body["code"] != "invalid_image" {
		t.Fatalf("invalid: %d %v", code, body)
	}
	if code, _ = h.do("POST", "/image/pull", `{"reference":"nginx@sha256:`+hexA+`"}`); code != 404 && code != 405 {
		t.Fatalf("pull without runtime: %d", code)
	}
}

func TestCheckAndPullImage(t *testing.T) {
	h := newImageHarness(t, true)
	code, body := h.do("GET", "/image?ref=nginx:1.27", "")
	if code != 200 || body["reference"] != "docker.io/library/nginx@sha256:"+strings.Repeat("d", 64) || body["resolved_from"] != "docker.io/library/nginx:1.27" || body["status"] != "available" {
		t.Fatalf("resolve: %d %v", code, body)
	}
	if code, body = h.do("GET", "/image?ref=private/app:1", ""); code != 404 || body["code"] != "image_not_found" {
		t.Fatalf("private: %d %v", code, body)
	}

	h.provider.image = rt.ImageMissing
	h.provider.pullGate = make(chan struct{})
	ref := "docker.io/library/hello-world@sha256:" + hexA
	if _, body = h.do("GET", "/image?ref="+ref, ""); body["status"] != "missing" {
		t.Fatalf("missing: %v", body)
	}
	if _, body = h.do("GET", "/image?ref=mcr.microsoft.com/windows/nanoserver@sha256:"+hexA, ""); body["status"] != "incompatible" || body["reason"] != "platform" || body["platform"] != "windows/amd64" {
		t.Fatalf("windows-only image: %v", body)
	}
	if code, body = h.do("POST", "/image/pull", `{"reference":"hello-world@sha256:`+hexA+`"}`); code != 202 || body["status"] != "pulling" || body["reference"] != ref {
		t.Fatalf("pull: %d %v", code, body)
	}
	if _, body = h.do("GET", "/image?ref="+ref, ""); body["status"] != "pulling" {
		t.Fatalf("pulling: %v", body)
	}
	if code, _ = h.do("POST", "/image/pull", `{"reference":"`+ref+`"}`); code != 202 {
		t.Fatalf("repeat: %d", code)
	}
	if code, body = h.do("POST", "/image/pull", `{"reference":"busybox@sha256:`+hexA+`"}`); code != 409 || body["code"] != "pull_busy" {
		t.Fatalf("busy: %d %v", code, body)
	}
	close(h.provider.pullGate)
	waitStatus(t, h, ref, "available")
	waitIdle(t, h)
	if h.provider.pulls.Load() != 1 {
		t.Fatalf("pulls: %d", h.provider.pulls.Load())
	}

	h.provider.pullGate = nil
	h.provider.imageMu.Lock()
	h.provider.image = rt.ImageMissing
	h.provider.imageMu.Unlock()
	h.provider.pullErr = rt.ErrImagePlatform
	h.do("POST", "/image/pull", `{"reference":"`+ref+`"}`)
	body = waitStatus(t, h, ref, "pull_failed")
	if body["reason"] != "platform" {
		t.Fatalf("failure reason: %v", body)
	}
}

func waitIdle(t *testing.T, h *imageHarness) {
	t.Helper()
	for i := 0; i < 200; i++ {
		h.s.runtime.pulls.mu.Lock()
		idle := h.s.runtime.pulls.active == ""
		h.s.runtime.pulls.mu.Unlock()
		if idle {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("pull never finished")
}

func waitStatus(t *testing.T, h *imageHarness, ref, want string) map[string]string {
	t.Helper()
	for i := 0; i < 200; i++ {
		if _, body := h.do("GET", "/image?ref="+ref, ""); body["status"] == want {
			return body
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("status never became %s", want)
	return nil
}

func TestCreateServiceChecksImageBeforeDispatch(t *testing.T) {
	ctx := context.Background()
	h := newImageHarness(t, true)
	app, err := h.s.store.CreateApplication(ctx, "hola", "docker.io/library/hello-world@sha256:"+hexA, 8080)
	if err != nil {
		t.Fatal(err)
	}
	path := "/applications/" + app.ID + "/service"
	for _, c := range []struct {
		state rt.ImageState
		code  int
		want  string
	}{{rt.ImageMissing, 409, "image_missing"}, {rt.ImageIncompatible, 400, "image_incompatible"}} {
		h.provider.image = c.state
		if code, body := h.do("POST", path, `{"revision":0,"acknowledge_no_network":true}`); code != c.code || body["code"] != c.want {
			t.Fatalf("%s: %d %v", c.state, code, body)
		}
	}
	h.provider.image = ""
	h.provider.imageErr = rt.ErrUnavailable
	if code, body := h.do("POST", path, `{"revision":0,"acknowledge_no_network":true}`); code != 503 || body["code"] != "runtime_unavailable" {
		t.Fatalf("unavailable: %d %v", code, body)
	}
	h.provider.imageErr = nil
	// Nothing was persisted: no intention, no mark, no configuration.
	if code, _ := h.do("GET", path, ""); code != 404 {
		t.Fatalf("an intention was reserved: %d", code)
	}
	if got, _ := h.s.store.Application(ctx, app.ID); got.ServiceUsed {
		t.Fatal("application marked before dispatch")
	}
	if snap, _ := h.vault.Get(ctx, app.ID); snap.Revision != 0 {
		t.Fatal("configuration initialized before the image check")
	}

	// Without variables, revision 0 creates an empty first revision.
	if code, body := h.do("POST", path, `{"revision":0,"acknowledge_no_network":true}`); code != 201 && code != 202 {
		t.Fatalf("create without variables: %d %v", code, body)
	}
	if snap, _ := h.vault.Get(ctx, app.ID); snap.Revision != 1 || len(snap.Variables) != 0 {
		t.Fatalf("empty revision: %+v", snap)
	}
	if h.provider.creates.Load() != 1 {
		t.Fatal("not dispatched")
	}

	// With variables saved, revision 0 is a conflict before dispatch.
	other, err := h.s.store.CreateApplication(ctx, "otra", "docker.io/library/hello-world@sha256:"+hexA, 8080)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.vault.Set(ctx, other.ID, 0, "NAME", "value", false); err != nil {
		t.Fatal(err)
	}
	if code, body := h.do("POST", "/applications/"+other.ID+"/service", `{"revision":0,"acknowledge_no_network":true}`); code != 409 || body["code"] != "revision_conflict_before_dispatch" {
		t.Fatalf("revision 0 with configuration: %d %v", code, body)
	}

	// A definition saved before normalization is canonicalized on create.
	legacy, err := h.s.store.CreateApplication(ctx, "antigua", "hello-world:tag@sha256:"+hexA, 8080)
	if err != nil {
		t.Fatal(err)
	}
	if code, body := h.do("POST", "/applications/"+legacy.ID+"/service", `{"revision":0,"acknowledge_no_network":true}`); code != 201 && code != 202 {
		t.Fatalf("legacy image: %d %v", code, body)
	}
}
