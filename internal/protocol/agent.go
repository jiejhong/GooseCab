package protocol

type AgentHello struct {
 AgentID string `json:"agent_id"`
 Name string `json:"name"`
 Role string `json:"role,omitempty"`
 ResumeSeq int64 `json:"resume_seq,omitempty"`
 Capabilities []string `json:"capabilities,omitempty"`
}
type AgentWelcome struct { SessionID string `json:"session_id"`; ServerTime string `json:"server_time"`; ResumeFrom int64 `json:"resume_from"` }
type AgentMessage struct { Type string `json:"type"`; RequestID string `json:"request_id,omitempty"`; CapabilityID string `json:"capability_id,omitempty"`; Payload any `json:"payload,omitempty"` }
// Agent nodes initiate outbound WSS connections; the gateway must not require inbound agent ports.
