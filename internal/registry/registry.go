// Package registry holds the set of loaded providers.
//
// The registry owns the current provider set, which is swapped wholesale on a
// hot reload. Everything else talks to Registry rather than to a package-level
// map, so a reload is a single Replaced call and not a scattering of updates.
package registry

import (
	"fmt"
	"sort"
	"sync"

	"keep-mirrors/internal/provider"
)

// Registry is the set of providers the UI can display.
type Registry struct {
	mu sync.RWMutex
	m  map[string]provider.Provider
}

// New returns an empty registry.
func New() *Registry {
	return &Registry{m: map[string]provider.Provider{}}
}

// Register adds a provider. A duplicate id replaces the previous entry,
// which is what user plugins need in order to override a built-in one.
func (r *Registry) Register(p provider.Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[p.ID()] = p
}

// Replace swaps the whole provider set atomically.
//
// A reload must not be observable halfway through: registering the new set one
// provider at a time would let a reader in another goroutine see a mix of old
// and new VMs, including two different versions of the same plugin.
func (r *Registry) Replace(ps []provider.Provider) {
	m := make(map[string]provider.Provider, len(ps))
	for _, p := range ps {
		m[p.ID()] = p
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m = m
}

// Get returns the provider with the given id.
func (r *Registry) Get(id string) (provider.Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.m[id]
	return p, ok
}

// All returns every provider in declaration order.
func (r *Registry) All() []provider.Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]provider.Provider, 0, len(r.m))
	for _, p := range r.m {
		out = append(out, p)
	}
	// Category first, then title, so the grouped list is stable.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Category() != out[j].Category() {
			return out[i].Category() < out[j].Category()
		}
		return out[i].Title() < out[j].Title()
	})
	return out
}

// ByCategory groups providers for the tree view.
func (r *Registry) ByCategory() map[provider.Category][]provider.Provider {
	out := map[provider.Category][]provider.Provider{}
	for _, p := range r.All() {
		out[p.Category()] = append(out[p.Category()], p)
	}
	return out
}

// Categories lists the categories that have at least one provider, in a
// fixed display order.
func (r *Registry) Categories() []provider.Category {
	order := []provider.Category{
		provider.CategoryLanguage,
		provider.CategoryContainer,
		provider.CategoryOS,
		provider.CategoryTool,
	}
	have := r.ByCategory()
	var out []provider.Category
	for _, c := range order {
		if len(have[c]) > 0 {
			out = append(out, c)
		}
	}
	return out
}

// ErrNoProvider is returned when an id is unknown.
func ErrNoProvider(id string) error { return fmt.Errorf("no provider registered: %s", id) }
