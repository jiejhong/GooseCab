package registry

import "sync"

type Capability struct { ID string `json:"id"`; Name string `json:"name"`; Description string `json:"description"`; Version string `json:"version"`; InputSchema any `json:"input_schema,omitempty"`; OutputSchema any `json:"output_schema,omitempty"`; Risk string `json:"risk,omitempty"` }
type Agent struct { ID string `json:"id"`; Name string `json:"name"`; Role string `json:"role,omitempty"`; Persona string `json:"persona,omitempty"`; Capabilities []Capability `json:"capabilities"` }
type Registry struct { mu sync.RWMutex; agents map[string]Agent }
func New()*Registry{return &Registry{agents:map[string]Agent{}}}
func(r *Registry) Upsert(a Agent){r.mu.Lock();r.agents[a.ID]=a;r.mu.Unlock()}
func(r *Registry) Get(id string)(Agent,bool){r.mu.RLock();defer r.mu.RUnlock();a,ok:=r.agents[id];return a,ok}
func(r *Registry) List()[]Agent{r.mu.RLock();defer r.mu.RUnlock();out:=make([]Agent,0,len(r.agents));for _,a:=range r.agents{out=append(out,a)};return out}
func(r *Registry) FindCapability(q string)[]Agent{r.mu.RLock();defer r.mu.RUnlock();var out []Agent;for _,a:=range r.agents{for _,c:=range a.Capabilities{if q==""||c.ID==q||c.Name==q{out=append(out,a);break}}};return out}
