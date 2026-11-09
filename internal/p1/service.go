package p1

import (
    "context"
    "fmt"
    "sync"
    "time"

    "github.com/jiejhong/GooseCab/internal/protocol"
    "github.com/jiejhong/GooseCab/internal/store"
)

// Invocation is the metadata for a capability call. Input and output stay on agents.
type Invocation struct {
    ID string `json:"id"`
    RoomID string `json:"room_id"`
    TaskID string `json:"task_id,omitempty"`
    CapabilityID string `json:"capability_id"`
    CallerAgentID string `json:"caller_agent_id"`
    TargetAgentID string `json:"target_agent_id"`
    InputRef string `json:"input_ref,omitempty"`
    OutputRef string `json:"output_ref,omitempty"`
    Status string `json:"status"`
    CreatedAt time.Time `json:"created_at"`
    UpdatedAt time.Time `json:"updated_at"`
}

type Progress struct { Percent int `json:"percent"`; Message string `json:"message,omitempty"` }
type Result struct { OutputRef string `json:"output_ref,omitempty"`; Error string `json:"error,omitempty"`; Status string `json:"status"` }

type TeachSession struct {
    ID string `json:"id"`
    TeacherAgentID string `json:"teacher_agent_id"`
    LearnerAgentID string `json:"learner_agent_id"`
    CapabilityID string `json:"capability_id"`
    Status string `json:"status"`
    SkillPackageID string `json:"skill_package_id,omitempty"`
    CreatedAt time.Time `json:"created_at"`
}

type SkillPackage struct {
    ID string `json:"id"`
    Name string `json:"name"`
    CapabilityID string `json:"capability_id"`
    Version string `json:"version"`
    Manifest map[string]any `json:"manifest,omitempty"`
    ArtifactRef string `json:"artifact_ref,omitempty"` // local agent ref, never file contents
    Status string `json:"status"`
    PreviousVersion string `json:"previous_version,omitempty"`
    CreatedAt time.Time `json:"created_at"`
}

type Evaluation struct { SkillPackageID string `json:"skill_package_id"`; Version string `json:"version"`; Status string `json:"status"`; Score float64 `json:"score,omitempty"`; Checks []string `json:"checks,omitempty"`; Message string `json:"message,omitempty"`; UpdatedAt time.Time `json:"updated_at"` }

type Service struct { mu sync.RWMutex; store *store.Store; inv map[string]Invocation; teaches map[string]TeachSession; skills map[string][]SkillPackage; evals map[string]Evaluation }
func New(s *store.Store) *Service { return &Service{store:s, inv:map[string]Invocation{}, teaches:map[string]TeachSession{}, skills:map[string][]SkillPackage{}, evals:map[string]Evaluation{}} }
func (s *Service) append(ctx context.Context, room, typ, task, cap, session, corr string, payload any) error { if s.store==nil{return nil}; seq,err:=s.store.NextSeq(ctx,room);if err!=nil{return err}; return s.store.Append(ctx,protocol.Event{EventID:fmt.Sprintf("%s-%d",typ,seq),RoomID:room,Seq:seq,Type:typ,TaskID:task,CapabilityID:cap,SessionID:session,CorrelationID:corr,TS:time.Now().UTC(),Payload:payload}) }
func (s *Service) CreateInvocation(ctx context.Context, v Invocation) (Invocation,error) { now:=time.Now().UTC();v.CreatedAt=now;v.UpdatedAt=now;if v.Status==""{v.Status="requested"};s.mu.Lock();s.inv[v.ID]=v;s.mu.Unlock();return v,s.append(ctx,v.RoomID,"capability.invoke.request",v.TaskID,v.CapabilityID,"",v.ID,v) }
func (s *Service) UpdateProgress(ctx context.Context,id string,p Progress) (Invocation,error) {s.mu.Lock();v,ok:=s.inv[id];if !ok{s.mu.Unlock();return v,fmt.Errorf("invocation not found")};v.Status="running";v.UpdatedAt=time.Now().UTC();s.inv[id]=v;s.mu.Unlock();return v,s.append(ctx,v.RoomID,"capability.invoke.progress",v.TaskID,v.CapabilityID,"",id,p)}
func (s *Service) Complete(ctx context.Context,id string,r Result)(Invocation,error){s.mu.Lock();v,ok:=s.inv[id];if !ok{s.mu.Unlock();return v,fmt.Errorf("invocation not found")};v.Status=r.Status;if v.Status==""{v.Status="completed"};v.OutputRef=r.OutputRef;v.UpdatedAt=time.Now().UTC();s.inv[id]=v;s.mu.Unlock();return v,s.append(ctx,v.RoomID,"capability.invoke.result",v.TaskID,v.CapabilityID,"",id,r)}
func (s *Service) StartTeach(ctx context.Context,t TeachSession)(TeachSession,error){if t.Status==""{t.Status="active"};if t.CreatedAt.IsZero(){t.CreatedAt=time.Now().UTC()};s.mu.Lock();s.teaches[t.ID]=t;s.mu.Unlock();return t,s.append(ctx,"", "teach.session.started","","",t.ID,"",t)}
func (s *Service) AddSkill(ctx context.Context,p SkillPackage)(SkillPackage,error){if p.Status==""{p.Status="active"};if p.CreatedAt.IsZero(){p.CreatedAt=time.Now().UTC()};s.mu.Lock();versions:=s.skills[p.ID];if len(versions)>0{p.PreviousVersion=versions[len(versions)-1].Version};s.skills[p.ID]=append(versions,p);s.mu.Unlock();return p,s.append(ctx,"","skill.package.published","",p.CapabilityID,"","",p)}
func (s *Service) Evaluate(ctx context.Context,e Evaluation)(Evaluation,error){e.UpdatedAt=time.Now().UTC();s.mu.Lock();s.evals[e.SkillPackageID+"@"+e.Version]=e;s.mu.Unlock();return e,s.append(ctx,"","skill.evaluation.updated","", "", "", "",e)}
func (s *Service) Rollback(ctx context.Context,id,version string)(SkillPackage,error){s.mu.Lock();vs:=s.skills[id];var found SkillPackage;for _,p:=range vs{if p.Version==version{found=p;break}};if found.ID==""{s.mu.Unlock();return found,fmt.Errorf("skill version not found")};found.Status="active";s.skills[id]=append(vs,found);s.mu.Unlock();return found,s.append(ctx,"","skill.package.rollback","",found.CapabilityID,"","",found)}
func (s *Service) Invocation(id string)(Invocation,bool){s.mu.RLock();defer s.mu.RUnlock();v,ok:=s.inv[id];return v,ok}
