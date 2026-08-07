package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

type Node struct {
	ID               string          `json:"id"`
	ComponentID      string          `json:"component_id"`
	ComponentVersion string          `json:"component_version,omitempty"`
	Config           json.RawMessage `json:"config,omitempty"`
}

type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Blueprint is an immutable, versioned state-flow definition. The first
// implementation executes an acyclic graph; deliberate iteration is created by
// a future bounded loop component, not implicit graph cycles.
type Blueprint struct {
	ID          string `json:"id"`
	Version     int    `json:"version"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Nodes       []Node `json:"nodes"`
	Edges       []Edge `json:"edges"`
	CreatedAt   int64  `json:"created_at"`
}

func (b Blueprint) Clone() Blueprint {
	clone := b
	clone.Nodes = append([]Node(nil), b.Nodes...)
	for i := range clone.Nodes {
		clone.Nodes[i].Config = append(json.RawMessage(nil), b.Nodes[i].Config...)
	}
	clone.Edges = append([]Edge(nil), b.Edges...)
	return clone
}

func (b Blueprint) Validate() error {
	if b.ID == "" || b.Version < 1 {
		return fmt.Errorf("blueprint id and positive version are required")
	}
	if len(b.Nodes) == 0 {
		return fmt.Errorf("blueprint %s has no nodes", b.ID)
	}
	nodes := make(map[string]struct{}, len(b.Nodes))
	for _, node := range b.Nodes {
		if node.ID == "" || node.ComponentID == "" {
			return fmt.Errorf("blueprint %s has node without id or component", b.ID)
		}
		if _, exists := nodes[node.ID]; exists {
			return fmt.Errorf("blueprint %s has duplicate node %s", b.ID, node.ID)
		}
		nodes[node.ID] = struct{}{}
	}
	indegree := make(map[string]int, len(nodes))
	adj := make(map[string][]string, len(nodes))
	for _, edge := range b.Edges {
		if _, ok := nodes[edge.From]; !ok {
			return fmt.Errorf("blueprint %s edge source %s does not exist", b.ID, edge.From)
		}
		if _, ok := nodes[edge.To]; !ok {
			return fmt.Errorf("blueprint %s edge target %s does not exist", b.ID, edge.To)
		}
		adj[edge.From] = append(adj[edge.From], edge.To)
		indegree[edge.To]++
	}
	queue := make([]string, 0, len(nodes))
	for id := range nodes {
		if indegree[id] == 0 {
			queue = append(queue, id)
		}
	}
	seen := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		seen++
		for _, next := range adj[id] {
			indegree[next]--
			if indegree[next] == 0 {
				queue = append(queue, next)
			}
		}
	}
	if seen != len(nodes) {
		return fmt.Errorf("blueprint %s contains a cycle", b.ID)
	}
	return nil
}

type BlueprintStore struct {
	mu       sync.RWMutex
	path     string
	versions map[string]map[int]Blueprint
	active   map[string]int
	onChange func([]Blueprint, map[string]int) error
}

func NewBlueprintStore() *BlueprintStore {
	return &BlueprintStore{
		versions: make(map[string]map[int]Blueprint),
		active:   make(map[string]int),
	}
}

func (s *BlueprintStore) Restore(blueprints []Blueprint, active map[string]int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.versions = make(map[string]map[int]Blueprint)
	s.active = make(map[string]int)
	for _, b := range blueprints {
		if err := b.Validate(); err != nil {
			return err
		}
		if s.versions[b.ID] == nil {
			s.versions[b.ID] = make(map[int]Blueprint)
		}
		s.versions[b.ID][b.Version] = b.Clone()
	}
	for id, version := range active {
		if _, ok := s.versions[id][version]; ok {
			s.active[id] = version
		}
	}
	return nil
}
func (s *BlueprintStore) SetOnChange(fn func([]Blueprint, map[string]int) error) {
	s.mu.Lock()
	s.onChange = fn
	s.mu.Unlock()
}
func (s *BlueprintStore) Persist() error { s.mu.Lock(); defer s.mu.Unlock(); return s.saveLocked() }

func NewBlueprintStoreAt(path string) (*BlueprintStore, error) {
	s := NewBlueprintStore()
	s.path = path
	if path == "" {
		return s, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("load blueprints: %w", err)
	}
	var persisted struct {
		Blueprints []Blueprint    `json:"blueprints"`
		Active     map[string]int `json:"active"`
	}
	if err := json.Unmarshal(b, &persisted); err != nil {
		return nil, fmt.Errorf("decode blueprints: %w", err)
	}
	for _, blueprint := range persisted.Blueprints {
		if err := blueprint.Validate(); err != nil {
			return nil, fmt.Errorf("load blueprint %s v%d: %w", blueprint.ID, blueprint.Version, err)
		}
		if s.versions[blueprint.ID] == nil {
			s.versions[blueprint.ID] = make(map[int]Blueprint)
		}
		s.versions[blueprint.ID][blueprint.Version] = blueprint.Clone()
	}
	for id, version := range persisted.Active {
		if _, ok := s.versions[id][version]; ok {
			s.active[id] = version
		}
	}
	return s, nil
}

func (s *BlueprintStore) Publish(b Blueprint, activate bool) error {
	if err := b.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.versions[b.ID] == nil {
		s.versions[b.ID] = make(map[int]Blueprint)
	}
	if _, exists := s.versions[b.ID][b.Version]; exists {
		return fmt.Errorf("blueprint %s v%d is immutable and already exists", b.ID, b.Version)
	}
	s.versions[b.ID][b.Version] = b.Clone()
	previousActive := s.active[b.ID]
	if activate || s.active[b.ID] == 0 {
		s.active[b.ID] = b.Version
	}
	if err := s.saveLocked(); err != nil {
		delete(s.versions[b.ID], b.Version)
		s.active[b.ID] = previousActive
		return err
	}
	return nil
}

func (s *BlueprintStore) Activate(id string, version int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.versions[id][version]; !ok {
		return fmt.Errorf("blueprint %s v%d not found", id, version)
	}
	previous := s.active[id]
	s.active[id] = version
	if err := s.saveLocked(); err != nil {
		s.active[id] = previous
		return err
	}
	return nil
}

func (s *BlueprintStore) ActiveVersions() map[string]int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]int, len(s.active))
	for id, version := range s.active {
		out[id] = version
	}
	return out
}

func (s *BlueprintStore) Get(id string, version int) (Blueprint, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if version == 0 {
		version = s.active[id]
	}
	b, ok := s.versions[id][version]
	if !ok {
		return Blueprint{}, fmt.Errorf("blueprint %s v%d not found", id, version)
	}
	return b.Clone(), nil
}

func (s *BlueprintStore) List() []Blueprint {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Blueprint
	for _, versions := range s.versions {
		for _, b := range versions {
			out = append(out, b.Clone())
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

func (s *BlueprintStore) saveLocked() error {
	var all []Blueprint
	for _, versions := range s.versions {
		for _, blueprint := range versions {
			all = append(all, blueprint.Clone())
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].ID != all[j].ID {
			return all[i].ID < all[j].ID
		}
		return all[i].Version < all[j].Version
	})
	activeCopy := make(map[string]int, len(s.active))
	for id, v := range s.active {
		activeCopy[id] = v
	}
	if s.onChange != nil {
		if err := s.onChange(all, activeCopy); err != nil {
			return err
		}
	}
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(struct {
		Blueprints []Blueprint    `json:"blueprints"`
		Active     map[string]int `json:"active"`
	}{Blueprints: all, Active: activeCopy}, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
