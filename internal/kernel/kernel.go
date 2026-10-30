// Package kernel coordinates trusted, statically compiled modules.
package kernel

import (
	"context"
	"fmt"
)

type Module interface {
	ID() string
	Dependencies() []string
	Start(context.Context) error
	Stop(context.Context) error
}

type Registry struct {
	modules map[string]Module
	order   []Module
	started []Module
}

func New() *Registry { return &Registry{modules: map[string]Module{}} }
func (r *Registry) Register(m Module) error {
	if len(r.started) > 0 {
		return fmt.Errorf("cannot register modules after startup")
	}
	if m.ID() == "" {
		return fmt.Errorf("module ID cannot be empty")
	}
	if _, ok := r.modules[m.ID()]; ok {
		return fmt.Errorf("duplicate module %q", m.ID())
	}
	r.modules[m.ID()] = m
	r.order = append(r.order, m)
	return nil
}
func (r *Registry) Start(ctx context.Context) error {
	if len(r.started) > 0 {
		return fmt.Errorf("modules already started")
	}
	visited := map[string]int{}
	ordered := []Module{}
	var visit func(string) error
	visit = func(id string) error {
		m, ok := r.modules[id]
		if !ok {
			return fmt.Errorf("missing module %q", id)
		}
		if visited[id] == 1 {
			return fmt.Errorf("dependency cycle at %q", id)
		}
		if visited[id] == 2 {
			return nil
		}
		visited[id] = 1
		for _, dep := range m.Dependencies() {
			if err := visit(dep); err != nil {
				return fmt.Errorf("module %q: %w", id, err)
			}
		}
		visited[id] = 2
		ordered = append(ordered, m)
		return nil
	}
	for _, m := range r.order {
		if err := visit(m.ID()); err != nil {
			return err
		}
	}
	for _, m := range ordered {
		if err := m.Start(ctx); err != nil {
			_ = r.Stop(context.WithoutCancel(ctx))
			return fmt.Errorf("start %q: %w", m.ID(), err)
		}
		r.started = append(r.started, m)
	}
	return nil
}
func (r *Registry) Stop(ctx context.Context) error {
	var first error
	for i := len(r.started) - 1; i >= 0; i-- {
		if err := r.started[i].Stop(ctx); err != nil && first == nil {
			first = fmt.Errorf("stop %q: %w", r.started[i].ID(), err)
		}
	}
	r.started = nil
	return first
}
