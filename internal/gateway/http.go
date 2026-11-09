package gateway

import("encoding/json";"net/http";"strings";"github.com/jiejhong/GooseCab/internal/registry")
type API struct{ Registry *registry.Registry }
func(a *API) Handler()http.Handler{m:=http.NewServeMux();m.HandleFunc("/api/agents",a.agents);m.HandleFunc("/api/capabilities",a.capabilities);return m}
func(a *API) agents(w http.ResponseWriter,r *http.Request){w.Header().Set("Content-Type","application/json");if r.Method==http.MethodPost{var x registry.Agent;if json.NewDecoder(r.Body).Decode(&x)!=nil||x.ID==""{http.Error(w,"invalid agent",400);return};a.Registry.Upsert(x);w.WriteHeader(201);json.NewEncoder(w).Encode(x);return};json.NewEncoder(w).Encode(a.Registry.List())}
func(a *API) capabilities(w http.ResponseWriter,r *http.Request){w.Header().Set("Content-Type","application/json");q:=r.URL.Query().Get("q");if strings.TrimSpace(q)==""{json.NewEncoder(w).Encode(a.Registry.List());return};json.NewEncoder(w).Encode(a.Registry.FindCapability(q))}
