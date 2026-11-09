package transfer

import("context";"fmt";"sync";"time";"github.com/jiejhong/GooseCab/internal/protocol";"github.com/jiejhong/GooseCab/internal/eventbus";"github.com/jiejhong/GooseCab/internal/store")
// Metadata describes a local file transfer. The gateway never receives file bytes.
type Metadata struct { RoomID string `json:"room_id,omitempty"`; ID string `json:"file_id"`; SourceAgent string `json:"source_agent"`; SourcePathRef string `json:"source_path_ref"`; TargetAgent string `json:"target_agent"`; TargetPathRef string `json:"target_path_ref"`; Size int64 `json:"size"`; SHA256 string `json:"sha256"`; Mode string `json:"mode"`; Offset int64 `json:"offset"`; Status string `json:"status"`; UpdatedAt time.Time `json:"updated_at"` }
type Service struct { mu sync.RWMutex; store *store.Store; bus *eventbus.Bus; items map[string]Metadata }
func New(s *store.Store,b *eventbus.Bus)*Service{return &Service{store:s,bus:b,items:map[string]Metadata{}}}
func(s *Service) Save(ctx context.Context,m Metadata,typ string)(Metadata,error){if m.Status==""{m.Status="requested"};m.UpdatedAt=time.Now().UTC();s.mu.Lock();s.items[m.ID]=m;s.mu.Unlock();if s.store==nil{return m,nil};room:=m.RoomID;if room==""{room="transfers"};seq,e:=s.store.NextSeq(ctx,room);if e!=nil{return m,e};evt:=protocol.Event{EventID:fmt.Sprintf("transfer-%s-%d",m.ID,seq),RoomID:room,Seq:seq,Type:typ,CorrelationID:m.ID,TS:m.UpdatedAt,Payload:m};e=s.store.Append(ctx,evt);if e==nil&&s.bus!=nil{s.bus.Publish(evt)};return m,e}
func(s *Service) Get(id string)(Metadata,bool){s.mu.RLock();defer s.mu.RUnlock();m,ok:=s.items[id];return m,ok}
