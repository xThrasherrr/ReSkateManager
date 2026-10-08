package instance

import (
	"cmp"
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
)

// Registry holds every managed instance.
type Registry struct {
	log *slog.Logger
	mu  sync.RWMutex
	all map[string]*Instance
	// Wire is called on each instance as it is added, before it can start.
	Wire func(*Instance)
}

// NewRegistry makes an empty registry.
func NewRegistry(log *slog.Logger) *Registry {
	return &Registry{log: log, all: map[string]*Instance{}}
}

// Add makes an instance for d and registers it. An ID already registered
// keeps its instance, which may be running; replacing it would leave that
// process with nothing to stop it.
func (r *Registry) Add(d Def) *Instance {
	r.mu.Lock()
	defer r.mu.Unlock()
	if in, ok := r.all[d.ID]; ok {
		r.log.Error("a server with this ID is registered already; keeping it", "instance", d.ID)
		return in
	}
	in := New(d, r.log)
	if r.Wire != nil {
		r.Wire(in)
	}
	r.all[d.ID] = in
	return in
}

// IDs lists every instance's ID, in List's order.
func (r *Registry) IDs() []string {
	list := r.List()
	ids := make([]string, len(list))
	for i, in := range list {
		ids[i] = in.ID()
	}
	return ids
}

// Remove forgets an instance; it must be stopped.
func (r *Registry) Remove(id string) {
	r.mu.Lock()
	delete(r.all, id)
	r.mu.Unlock()
}

// Get finds an instance by ID.
func (r *Registry) Get(id string) (*Instance, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	in, ok := r.all[id]
	return in, ok
}

// List returns instances in a stable order (by name, then id).
func (r *Registry) List() []*Instance {
	r.mu.RLock()
	out := make([]*Instance, 0, len(r.all))
	for _, in := range r.all {
		out = append(out, in)
	}
	r.mu.RUnlock()
	// Each Def takes its instance's lock: read them once, not per comparison.
	defs := make(map[*Instance]Def, len(out))
	for _, in := range out {
		defs[in] = in.Def()
	}
	slices.SortFunc(out, func(x, y *Instance) int {
		a, b := defs[x], defs[y]
		return cmp.Or(strings.Compare(a.Name, b.Name), strings.Compare(a.ID, b.ID))
	})
	return out
}

// StopAll stops every running server in parallel, for manager shutdown.
func (r *Registry) StopAll(timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var wg sync.WaitGroup
	for _, in := range r.List() {
		wg.Go(func() {
			if err := in.Stop(ctx); err != nil {
				in.Kill()
			}
		})
	}
	wg.Wait()
}
