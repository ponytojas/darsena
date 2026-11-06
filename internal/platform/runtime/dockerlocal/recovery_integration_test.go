package dockerlocal

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ponytojas/darsena/internal/platform/runtime/execution"
	"github.com/ponytojas/darsena/internal/platform/runtime/intent"
	"github.com/ponytojas/darsena/internal/platform/runtime/reconcile"
	"github.com/ponytojas/darsena/internal/platform/runtime/recovery"
)

// recordingEngine is a local in-process Engine double. It records the actual
// protocol requests so recovery cannot silently introduce a runtime mutation.
type recordingEngine struct {
	engine  *engine
	methods []string
}

func (r *recordingEngine) RoundTrip(req *http.Request) (*http.Response, error) {
	r.methods = append(r.methods, req.Method+" "+req.URL.Path)
	return r.engine.RoundTrip(req)
}

func TestPersistedIntentRecoveryAfterLostCreateResponse(t *testing.T) {
	for _, tc := range []struct {
		name  string
		alter func(*engine)
		want  reconcile.Observation
	}{
		{"created", func(*engine) {}, reconcile.Created},
		{"absent", func(e *engine) { e.container = nil }, reconcile.Absent},
		{"foreign owner", func(e *engine) {
			e.container["Config"].(map[string]any)["Labels"].(map[string]any)[prefix+"installation"] = "foreign"
		}, reconcile.Conflict},
		{"changed fingerprint", func(e *engine) {
			e.container["Config"].(map[string]any)["Labels"].(map[string]any)[prefix+"fingerprint"] = "foreign"
		}, reconcile.Conflict},
		{"changed image", func(e *engine) { e.container["Image"] = "sha256:" + strings.Repeat("d", 64) }, reconcile.Conflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			if err := os.Chmod(dir, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "intents.db")
			store, err := intent.Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			ref, err := PrepareRef(spec())
			if err != nil {
				t.Fatal(err)
			}
			e := fake()
			e.loseCreate = true
			transport := &recordingEngine{engine: e}
			adapter := newAdapter(transport)
			created, err := execution.Create(ctx, store, PrepareRef, adapter, spec())
			if !errors.Is(err, execution.ErrUnknown) || created.Intent.Ref != ref || strings.Contains(err.Error(), "SENSITIVE") {
				t.Fatalf("lost response: %#v %v", created, err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			tc.alter(e)
			store, err = intent.Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			transport.methods = nil
			result, err := recovery.Pass(ctx, store, adapter, nil, 10, time.Second)
			if err != nil || result.Processed != 1 || !result.Complete || result.Conflicts != 0 {
				t.Fatalf("recovery: %#v %v", result, err)
			}
			for _, method := range transport.methods {
				if !strings.HasPrefix(method, "GET ") {
					t.Fatalf("recovery mutated runtime: %s", method)
				}
			}
			if len(transport.methods) == 0 {
				t.Fatal("recovery did not inspect runtime")
			}
			observed, err := store.Get(ctx, ref.Identity)
			if err != nil || observed.Phase != intent.Observed || observed.Observation != tc.want {
				t.Fatalf("observation: %#v %v", observed, err)
			}
			if _, err = store.MarkDispatched(ctx, ref.Identity, observed.Version); !errors.Is(err, intent.ErrConflict) {
				t.Fatalf("observation authorized a new dispatch: %v", err)
			}
			if e.posts != 1 || e.startCalls != 0 {
				t.Fatalf("unexpected mutations: create=%d start=%d", e.posts, e.startCalls)
			}
		})
	}
}
