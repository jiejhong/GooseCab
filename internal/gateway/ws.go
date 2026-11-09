package gateway

import (
 "encoding/json"
 "net/http"
 "os"
 "strconv"
 "github.com/gorilla/websocket"
 "github.com/jiejhong/GooseCab/internal/store"
)

type WS struct{ Store *store.Store }
var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool {
 origin := r.Header.Get("Origin")
 return origin == "" || origin == r.Header.Get("Host") || os.Getenv("GOOSECAB_ALLOW_CROSS_ORIGIN") == "1"
}}
func (w *WS) Handler(h http.Handler) http.Handler { return http.HandlerFunc(func(rw http.ResponseWriter,r *http.Request) {
 if r.URL.Path != "/api/v1/agent/connect" && r.URL.Path != "/ws" { h.ServeHTTP(rw,r); return }
 expected := os.Getenv("GOOSECAB_AGENT_TOKEN")
 if expected != "" && r.Header.Get("Authorization") != "Bearer "+expected { http.Error(rw,"unauthorized",http.StatusUnauthorized); return }
 room := r.URL.Query().Get("room_id"); after,_ := strconv.ParseInt(r.URL.Query().Get("resume_seq"),10,64)
 c,err := upgrader.Upgrade(rw,r,nil); if err != nil{return}; defer c.Close()
 for { var msg map[string]any; if err:=c.ReadJSON(&msg); err!=nil{return}; if msg["type"]=="agent.hello" { _=c.WriteJSON(map[string]any{"type":"agent.welcome","resume_from":after,"room_id":room}) } }
})}
func JSON(v any) []byte { b,_:=json.Marshal(v); return b }
