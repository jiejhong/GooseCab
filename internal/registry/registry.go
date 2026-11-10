package registry

import (
	"sort"
	"sync"
)

type Capability struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Version      string `json:"version"`
	InputSchema  any    `json:"input_schema,omitempty"`
	OutputSchema any    `json:"output_schema,omitempty"`
	Risk         string `json:"risk,omitempty"`
	Manifest     any    `json:"manifest,omitempty"`
}
type Agent struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Role         string       `json:"role,omitempty"`
	Persona      string       `json:"persona,omitempty"`
	Capabilities []Capability `json:"capabilities"`
}
type Registry struct {
	mu     sync.RWMutex
	agents map[string]Agent
}

func New() *Registry               { return &Registry{agents: map[string]Agent{}} }
func clone(a Agent) Agent          { a.Capabilities = append([]Capability{}, a.Capabilities...); return a }
func (r *Registry) Upsert(a Agent) { r.mu.Lock(); defer r.mu.Unlock(); r.agents[a.ID] = clone(a) }
func (r *Registry) Get(id string) (Agent, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.agents[id]
	return clone(a), ok
}
func (r *Registry) List() []Agent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Agent, 0, len(r.agents))
	for _, a := range r.agents {
		out = append(out, clone(a))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func (r *Registry) FindCapability(q string) []Agent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Agent, 0)
	for _, a := range r.agents {
		for _, c := range a.Capabilities {
			if q == "" || c.ID == q || c.Name == q {
				out = append(out, clone(a))
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// RegisterHello replaces the advertised capability set on reconnect while
// preserving richer metadata for capability IDs the agent still advertises.
// Registry is a gateway-wide metadata catalog, not an online-presence service.
func (r *Registry) RegisterHello(id, name, role string, ids []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.agents[id]
	a.ID = id
	if name != "" {
		a.Name = name
	}
	if role != "" {
		a.Role = role
	}
	previous := map[string]Capability{}
	for _, c := range a.Capabilities {
		previous[c.ID] = c
	}
	a.Capabilities = make([]Capability, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		c := previous[id]
		c.ID = id
		a.Capabilities = append(a.Capabilities, c)
	}
	r.agents[a.ID] = a
}

// Announce atomically merges one capability without dropping concurrent ones.
func (r *Registry) Announce(id string, c Capability) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.agents[id]
	a.ID = id
	a = clone(a)
	for i, old := range a.Capabilities {
		if old.ID == c.ID {
			if c.Name != "" {
				old.Name = c.Name
			}
			if c.Version != "" {
				old.Version = c.Version
			}
			if c.Manifest != nil {
				old.Manifest = c.Manifest
			}
			a.Capabilities[i] = old
			r.agents[id] = a
			return
		}
	}
	a.Capabilities = append(a.Capabilities, c)
	r.agents[id] = a
}
