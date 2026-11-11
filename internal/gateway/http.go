package gateway

import (
	"encoding/json"
	"github.com/jiejhong/GooseCab/internal/protocol"
	"github.com/jiejhong/GooseCab/internal/registry"
	"net/http"
)

type API struct{ Registry *registry.Registry }

func (a *API) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("/api/agents", a.agents)
	m.HandleFunc("/api/capabilities", a.capabilities)
	return m
}
func (a *API) agents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodPost {
		var x registry.Agent
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&x) != nil || x.ID == "" {
			http.Error(w, "invalid agent", 400)
			return
		}
		for i, c := range x.Capabilities {
			raw, _ := json.Marshal(c.InputSchema)
			if c.InputSchema == nil {
				raw = nil
			}
			v := protocol.CapabilityAnnounce{CapabilityID: c.ID, Name: c.Name, Version: c.Version, Kind: c.Kind, Description: c.Description, Callable: c.Callable, InputSchema: raw, Skill: c.Skill, MCP: c.MCP, Knowledge: c.Knowledge, Manifest: c.Manifest}
			if protocol.NormalizeAnnouncement(&v) != nil {
				http.Error(w, "invalid capability metadata", 400)
				return
			}
			x.Capabilities[i] = registry.CapabilityFromAnnouncement(v)
		}
		a.Registry.Upsert(x)
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(x)
		return
	}
	json.NewEncoder(w).Encode(a.Registry.List())
}
func (a *API) capabilities(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	q := r.URL.Query().Get("q")
	kind := r.URL.Query().Get("kind")
	if kind != "" && kind != "text" && kind != "skill" && kind != "mcp" && kind != "knowledge" {
		http.Error(w, "invalid kind", 400)
		return
	}
	json.NewEncoder(w).Encode(a.Registry.SearchCapabilities(q, kind))
}
