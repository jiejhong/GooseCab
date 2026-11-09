package p2

import (
 "context"
 "fmt"
 "sync"
 "time"
 "github.com/jiejhong/GooseCab/internal/protocol"
 "github.com/jiejhong/GooseCab/internal/registry"
 "github.com/jiejhong/GooseCab/internal/store"
)

type Task struct { ID string `json:"id"`; PlanID string `json:"plan_id"`; ParentID string `json:"parent_id,omitempty"`; DependsOn []string `json:"depends_on,omitempty"`; CapabilityID string `json:"capability_id"`; AgentID string `json:"agent_id,omitempty"`; Status string `json:"status"`; Attempt int `json:"attempt"`; MaxAttempts int `json:"max_attempts"`; Error string `json:"error,omitempty"`; CreatedAt time.Time `json:"created_at"`; UpdatedAt time.Time `json:"updated_at"` }
type Plan struct { ID string `json:"id"`; Goal string `json:"goal"`; Strategy string `json:"strategy,omitempty"`; TaskIDs []string `json:"task_ids"`; Status string `json:"status"`; CreatedAt time.Time `json:"created_at"` }
type Blackboard struct { ID string `json:"id"`; PlanID string `json:"plan_id,omitempty"`; Keys []string `json:"keys,omitempty"`; Scope string `json:"scope"`; CreatedAt time.Time `json:"created_at"` }
type PrivateSession struct { ID string `json:"id"`; PlanID string `json:"plan_id,omitempty"`; AgentIDs []string `json:"agent_ids"`; CreatedAt time.Time `json:"created_at"` }
type ProviderAdapter interface { Name() string; Invoke(context.Context, Task) error }
type Service struct { mu sync.RWMutex; store *store.Store; registry *registry.Registry; plans map[string]Plan; tasks map[string]Task; boards map[string]Blackboard; sessions map[string]PrivateSession }
func New(s *store.Store,r *registry.Registry)*Service{return &Service{store:s,registry:r,plans:map[string]Plan{},tasks:map[string]Task{},boards:map[string]Blackboard{},sessions:map[string]PrivateSession{}}}
func(s *Service) append(ctx context.Context,room,typ,task string,p any)error{if s.store==nil{return nil};seq,e:=s.store.NextSeq(ctx,room);if e!=nil{return e};return s.store.Append(ctx,protocol.Event{EventID:fmt.Sprintf("%s-%d",typ,seq),RoomID:room,Seq:seq,Type:typ,TaskID:task,TS:time.Now().UTC(),Payload:p})}
func(s *Service) CreatePlan(ctx context.Context,p Plan)(Plan,error){if p.Status==""{p.Status="planned"};if p.CreatedAt.IsZero(){p.CreatedAt=time.Now().UTC()};s.mu.Lock();s.plans[p.ID]=p;s.mu.Unlock();return p,s.append(ctx,"", "plan.created","",p)}
func(s *Service) AddTask(ctx context.Context,t Task)(Task,error){if t.Status==""{t.Status="pending"};if t.MaxAttempts<1{t.MaxAttempts=3};if t.CreatedAt.IsZero(){t.CreatedAt=time.Now().UTC()};t.UpdatedAt=t.CreatedAt;s.mu.Lock();s.tasks[t.ID]=t;if p,ok:=s.plans[t.PlanID];ok{p.TaskIDs=append(p.TaskIDs,t.ID);s.plans[t.PlanID]=p};s.mu.Unlock();return t,s.append(ctx,"","task.created",t.ID,t)}
func(s *Service) Retry(ctx context.Context,id,agent string)(Task,error){s.mu.Lock();t,ok:=s.tasks[id];if !ok{s.mu.Unlock();return t,fmt.Errorf("task not found")};if t.Attempt>=t.MaxAttempts{s.mu.Unlock();return t,fmt.Errorf("retry limit reached")};t.Attempt++;t.AgentID=agent;t.Status="queued";t.Error="";t.UpdatedAt=time.Now().UTC();s.tasks[id]=t;s.mu.Unlock();return t,s.append(ctx,"","task.retry",id,t)}
func(s *Service) Resolve(q string)[]registry.Agent{if s.registry==nil{return nil};return s.registry.FindCapability(q)}
func(s *Service) AddBoard(ctx context.Context,b Blackboard)(Blackboard,error){if b.Scope==""{b.Scope="plan"};if b.CreatedAt.IsZero(){b.CreatedAt=time.Now().UTC()};s.mu.Lock();s.boards[b.ID]=b;s.mu.Unlock();return b,s.append(ctx,"","blackboard.created","",b)}
func(s *Service) AddSession(ctx context.Context,v PrivateSession)(PrivateSession,error){if v.CreatedAt.IsZero(){v.CreatedAt=time.Now().UTC()};s.mu.Lock();s.sessions[v.ID]=v;s.mu.Unlock();return v,s.append(ctx,"","private.session.created","",v)}
