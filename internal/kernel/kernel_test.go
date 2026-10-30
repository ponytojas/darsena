package kernel

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type fake struct {
	id    string
	deps  []string
	fail  bool
	trace *[]string
}

func (f fake) ID() string             { return f.id }
func (f fake) Dependencies() []string { return f.deps }
func (f fake) Start(context.Context) error {
	if f.fail {
		return errors.New("failed")
	}
	*f.trace = append(*f.trace, "start "+f.id)
	return nil
}
func (f fake) Stop(context.Context) error { *f.trace = append(*f.trace, "stop "+f.id); return nil }
func TestDependencies(t *testing.T) {
	for _, tc := range []struct {
		name    string
		modules []fake
	}{{"missing", []fake{{id: "a", deps: []string{"b"}}}}, {"cycle", []fake{{id: "a", deps: []string{"b"}}, {id: "b", deps: []string{"a"}}}}} {
		t.Run(tc.name, func(t *testing.T) {
			r := New()
			for _, m := range tc.modules {
				if err := r.Register(m); err != nil {
					t.Fatal(err)
				}
			}
			if err := r.Start(context.Background()); err == nil {
				t.Fatal("accepted invalid dependencies")
			}
		})
	}
}
func TestLifecycle(t *testing.T) {
	trace := []string{}
	r := New()
	_ = r.Register(fake{id: "app", deps: []string{"platform"}, trace: &trace})
	_ = r.Register(fake{id: "platform", trace: &trace})
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"start platform", "start app", "stop app", "stop platform"}
	if !reflect.DeepEqual(trace, want) {
		t.Fatalf("%v", trace)
	}
}
func TestStartupRollback(t *testing.T) {
	trace := []string{}
	r := New()
	_ = r.Register(fake{id: "a", trace: &trace})
	_ = r.Register(fake{id: "b", deps: []string{"a"}, trace: &trace, fail: true})
	if err := r.Start(context.Background()); err == nil {
		t.Fatal("expected failure")
	}
	if !reflect.DeepEqual(trace, []string{"start a", "stop a"}) {
		t.Fatal(trace)
	}
}
