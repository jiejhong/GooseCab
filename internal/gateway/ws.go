package gateway

import (
    "encoding/json"
    "net/http"
    "os"
    "strconv"
    "sync"
    "time"

    "github.com/gorilla/websocket"
    "github.com/jiejhong/GooseCab/internal/eventbus"
    "github.com/jiejhong/GooseCab/internal/protocol"
    "github.com/jiejhong/GooseCab/internal/store"
)

type WS struct { Store *store.Store; Bus *eventbus.Bus }
var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { origin:=r.Header.Get("Origin"); return origin=="" || origin==r.Header.Get("Host") || os.Getenv("GOOSECAB_ALLOW_CROSS_ORIGIN")=="1" }}

func (w *WS) Handler(h http.Handler) http.Handler { return http.HandlerFunc(func(rw http.ResponseWriter,r *http.Request) {
    if r.URL.Path!="/api/v1/agent/connect" && r.URL.Path!="/ws" { h.ServeHTTP(rw,r); return }
    expected:=os.Getenv("GOOSECAB_AGENT_TOKEN"); if expected!="" && r.Header.Get("Authorization")!="Bearer "+expected { http.Error(rw,"unauthorized",http.StatusUnauthorized); return }
    room:=r.URL.Query().Get("room_id"); after,_:=strconv.ParseInt(r.URL.Query().Get("resume_seq"),10,64)
    c,err:=upgrader.Upgrade(rw,r,nil); if err!=nil{return}; defer c.Close()
    if w.Bus==nil { w.Bus=eventbus.New() }; sub:=w.Bus.Subscribe(64); defer w.Bus.Unsubscribe(sub)
    var writeMu sync.Mutex; writeJSON:=func(v any)error{writeMu.Lock();defer writeMu.Unlock();return c.WriteJSON(v)}
    if events,e:=w.Store.EventsAfter(r.Context(),room,after);e==nil { for _,evt:=range events { if writeJSON(evt)!=nil{return} } }
    done:=make(chan struct{}); defer close(done)
    go func(){ for { select { case evt,ok:=<-sub: if !ok{return}; if evt.RoomID==room && evt.Seq>after { if writeJSON(evt)!=nil{return} }; case <-done:return } } }()
    agentID:=""
    publish:=func(typ,capID,corr string,payload any) { if room=="" {return}; seq,e:=w.Store.NextSeq(r.Context(),room); if e!=nil{return}; evt:=protocol.Event{EventID:typ+"-"+strconv.FormatInt(seq,10),RoomID:room,Seq:seq,Type:typ,CapabilityID:capID,CorrelationID:corr,Actor:protocol.Actor{Kind:"agent",ID:agentID},TS:time.Now().UTC(),Payload:payload}; if w.Store.Append(r.Context(),evt)==nil {w.Bus.Publish(evt)} }
    for { var msg map[string]any; if c.ReadJSON(&msg)!=nil{return}; typ,_:=msg["type"].(string)
        switch typ {
        case "agent.hello":
            b,_:=json.Marshal(msg); var hello protocol.AgentHello; _=json.Unmarshal(b,&hello); agentID=hello.AgentID
            _=writeJSON(map[string]any{"type":"agent.welcome","resume_from":after,"room_id":room})
            publish("agent.hello","","",hello)
            for _,cap:=range hello.Capabilities { publish("capability.announce",cap,"",map[string]any{"agent_id":agentID,"capability_id":cap}) }
        case "capability.announce":
            var v protocol.CapabilityAnnounce; b,_:=json.Marshal(msg); if json.Unmarshal(b,&v)==nil { if v.AgentID==""{v.AgentID=agentID}; publish("capability.announce",v.CapabilityID,"",v) }
        case "capability.invoke":
            var v protocol.CapabilityInvoke; b,_:=json.Marshal(msg); if json.Unmarshal(b,&v)==nil { publish("capability.invoke",v.CapabilityID,v.RequestID,v) }
        case "capability.progress":
            var v protocol.CapabilityProgress; b,_:=json.Marshal(msg); if json.Unmarshal(b,&v)==nil { publish("capability.progress","",v.RequestID,v) }
        case "capability.result":
            var v protocol.CapabilityResult; b,_:=json.Marshal(msg); if json.Unmarshal(b,&v)==nil { publish("capability.result","",v.RequestID,v) }
        case "transfer.request", "transfer.accepted", "transfer.progress", "transfer.completed", "transfer.failed":
            corr,_:=msg["file_id"].(string); if corr=="" { corr,_=msg["transfer_id"].(string) }; publish(typ,"",corr,msg)
        case "event.publish":
            if raw,ok:=msg["event"];ok { b,_:=json.Marshal(raw); var evt protocol.Event; if json.Unmarshal(b,&evt)==nil && evt.RoomID!="" && evt.Type!="" { seq,e:=w.Store.NextSeq(r.Context(),evt.RoomID); if e==nil {evt.Seq=seq;if evt.EventID==""{evt.EventID=evt.Type+"-"+strconv.FormatInt(seq,10)};if evt.TS.IsZero(){evt.TS=time.Now().UTC()};if w.Store.Append(r.Context(),evt)==nil{w.Bus.Publish(evt)}} } }
        }
    }
}) }
func JSON(v any)[]byte{b,_:=json.Marshal(v);return b}
