package protocol

type AgentHello struct {
	AgentID      string   `json:"agent_id"`
	Name         string   `json:"name"`
	Role         string   `json:"role,omitempty"`
	ResumeSeq    int64    `json:"resume_seq,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}
type AgentWelcome struct {
	SessionID  string `json:"session_id"`
	ServerTime string `json:"server_time"`
	ResumeFrom int64  `json:"resume_from"`
}
type AgentMessage struct {
	Type         string `json:"type"`
	RequestID    string `json:"request_id,omitempty"`
	CapabilityID string `json:"capability_id,omitempty"`
	Payload      any    `json:"payload,omitempty"`
}
type CapabilityAnnounce struct {
	AgentID      string `json:"agent_id"`
	CapabilityID string `json:"capability_id"`
	Name         string `json:"name,omitempty"`
	Version      string `json:"version,omitempty"`
	Manifest     any    `json:"manifest,omitempty"`
}
type CapabilityInvoke struct {
	Route         []string `json:"route,omitempty"`
	RequestID     string   `json:"request_id"`
	CapabilityID  string   `json:"capability_id"`
	TargetAgentID string   `json:"target_agent_id,omitempty"`
	Payload       any      `json:"payload,omitempty"`
}
type CapabilityProgress struct {
	Status    string `json:"status,omitempty"`
	RequestID string `json:"request_id"`
	Percent   int    `json:"percent,omitempty"`
	Message   string `json:"message,omitempty"`
}
type TextOutput struct {
	Text      string `json:"text"`
	MediaType string `json:"media_type"`
}
type CapabilityResult struct {
	ResultAvailable bool        `json:"result_available"`
	Output          *TextOutput `json:"output,omitempty"`
	RequestID       string      `json:"request_id"`
	Status          string      `json:"status"`
	OutputRef       string      `json:"output_ref,omitempty"`
	Error           string      `json:"error,omitempty"`
}

// Agent nodes initiate outbound WSS connections; the gateway must not require inbound agent ports.
