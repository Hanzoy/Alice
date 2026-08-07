package core

import (
	"fmt"
	"sort"
	"sync"

	"alice/pkg/component"
)

// Registry keeps every loaded version and an atomic active pointer per ID.
// Existing calls retain their resolved instance; later calls see activation changes.
type Registry struct {
	mu       sync.RWMutex
	versions map[string]map[string]component.Component
	active   map[string]string
}

func NewRegistry() *Registry {
	return &Registry{
		versions: make(map[string]map[string]component.Component),
		active:   make(map[string]string),
	}
}

func (r *Registry) Register(c component.Component) error {
	if c == nil {
		return fmt.Errorf("register component: nil component")
	}
	d := c.Descriptor()
	if d.ID == "" || d.Version == "" {
		return fmt.Errorf("register component: id and version are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.versions[d.ID] == nil {
		r.versions[d.ID] = make(map[string]component.Component)
	}
	r.versions[d.ID][d.Version] = c
	if r.active[d.ID] == "" {
		r.active[d.ID] = d.Version
	}
	return nil
}

func (r *Registry) Activate(id, version string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	versions := r.versions[id]
	if versions == nil || versions[version] == nil {
		return fmt.Errorf("activate component %s@%s: version not registered", id, version)
	}
	r.active[id] = version
	return nil
}

func (r *Registry) Resolve(id, version string) (component.Component, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if version == "" {
		version = r.active[id]
	}
	c := r.versions[id][version]
	if c == nil {
		return nil, fmt.Errorf("resolve component %s@%s: not found", id, version)
	}
	return c, nil
}

type RegisteredComponent struct {
	component.Descriptor
	Active bool `json:"active"`
}

func (r *Registry) List() []RegisteredComponent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []RegisteredComponent
	for id, versions := range r.versions {
		for version, c := range versions {
			out = append(out, RegisteredComponent{
				Descriptor: c.Descriptor(),
				Active:     r.active[id] == version,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return out[i].Version < out[j].Version
	})
	return out
}
