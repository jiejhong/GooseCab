package protocol

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

const MaxCapabilityPayloadBytes = 16 * 1024
const (
	CapabilityKindText      = "text"
	CapabilityKindSkill     = "skill"
	CapabilityKindMCP       = "mcp"
	CapabilityKindKnowledge = "knowledge"
)

type SkillDescriptor struct {
	Digest         string `json:"digest"`
	Version        string `json:"version"`
	EntrypointKind string `json:"entrypoint_kind"`
}
type MCPDescriptor struct {
	ServerName           string `json:"server_name"`
	ToolName             string `json:"tool_name"`
	ConnectionManifestID string `json:"connection_manifest_id"`
}
type KnowledgeDescriptor struct {
	Collection  string `json:"collection"`
	SharePolicy string `json:"share_policy"`
}

var logicalID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
var digestID = regexp.MustCompile(`^(sha256:)?[a-fA-F0-9]{64}$`)

func CapabilityKind(id string) string {
	switch {
	case strings.HasPrefix(id, "cap.skill."):
		return CapabilityKindSkill
	case strings.HasPrefix(id, "cap.mcp."):
		return CapabilityKindMCP
	case strings.HasPrefix(id, "cap.knowledge."):
		return CapabilityKindKnowledge
	default:
		return CapabilityKindText
	}
}

// Namespace membership selects protocol guards, never local execution permission.
func IsExecutableCapability(id string) bool {
	return IsExecutableTextCapability(id) || ((strings.HasPrefix(id, "cap.mcp.") && len(id) > len("cap.mcp.")) || (strings.HasPrefix(id, "cap.knowledge.") && len(id) > len("cap.knowledge."))) && logicalID.MatchString(id)
}
func ValidateCapabilityPayload(id string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil || len(raw) > MaxCapabilityPayloadBytes {
		return fmt.Errorf("payload must be JSON at most 16384 bytes")
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return fmt.Errorf("payload must be a JSON object")
	}
	if IsExecutableTextCapability(id) {
		var text string
		if len(obj) != 1 || len(obj["text"]) == 0 || obj["text"][0] != '"' || json.Unmarshal(obj["text"], &text) != nil || !utf8.ValidString(text) || len(text) > 4096 {
			return fmt.Errorf("text payload must contain only text of at most 4096 UTF-8 bytes")
		}
	}
	return nil
}

// NormalizeAnnouncement validates public metadata. Credentials, connection details,
// package files and knowledge records belong exclusively in local providers.
func NormalizeAnnouncement(a *CapabilityAnnounce) error {
	if !logicalID.MatchString(a.CapabilityID) {
		return fmt.Errorf("invalid capability identifier")
	}
	if a.Kind == "" {
		a.Kind = CapabilityKind(a.CapabilityID)
		a.Callable = a.Callable || (a.Kind == CapabilityKindText && IsExecutableTextCapability(a.CapabilityID))
	}
	if a.Kind != CapabilityKind(a.CapabilityID) {
		return fmt.Errorf("kind must match capability namespace")
	}
	if len(a.Name) > 128 || len(a.Version) > 64 || len(a.Description) > 1024 || !utf8.ValidString(a.Description) {
		return fmt.Errorf("metadata exceeds limits")
	}
	if a.Manifest != nil {
		return fmt.Errorf("arbitrary manifests are not public metadata; use typed descriptors")
	}
	n := 0
	if a.Skill != nil {
		n++
	}
	if a.MCP != nil {
		n++
	}
	if a.Knowledge != nil {
		n++
	}
	if n > 1 {
		return fmt.Errorf("only one descriptor is allowed")
	}
	if s := a.Skill; s != nil {
		if a.Kind != "skill" || !digestID.MatchString(s.Digest) || !logicalID.MatchString(s.Version) || (s.EntrypointKind != "manual" && s.EntrypointKind != "text-recipe") || (s.EntrypointKind == "manual" && a.Callable) {
			return fmt.Errorf("invalid skill descriptor")
		}
	}
	if m := a.MCP; m != nil {
		if a.Kind != "mcp" || !logicalID.MatchString(m.ServerName) || !logicalID.MatchString(m.ToolName) || !logicalID.MatchString(m.ConnectionManifestID) {
			return fmt.Errorf("MCP descriptor requires logical identifiers only")
		}
	}
	if k := a.Knowledge; k != nil {
		if a.Kind != "knowledge" || !logicalID.MatchString(k.Collection) || !logicalID.MatchString(k.SharePolicy) {
			return fmt.Errorf("knowledge descriptor requires logical collection and share policy")
		}
	}
	if a.Callable && ((a.Kind == CapabilityKindSkill && a.Skill == nil) || (a.Kind == CapabilityKindMCP && a.MCP == nil) || (a.Kind == CapabilityKindKnowledge && a.Knowledge == nil)) {
		return fmt.Errorf("callable first-class capabilities require a typed descriptor")
	}
	if a.Callable && !IsExecutableCapability(a.CapabilityID) {
		return fmt.Errorf("capability does not support bounded execution")
	}
	if len(a.InputSchema) > 4096 {
		return fmt.Errorf("input schema too large")
	}
	if len(a.InputSchema) > 0 {
		var schema map[string]any
		if json.Unmarshal(a.InputSchema, &schema) != nil || schema == nil {
			return fmt.Errorf("input schema must be an object")
		}
		if err := validatePublicSchema(schema); err != nil {
			return err
		}
	}
	return nil
}
func validatePublicSchema(s map[string]any) error {
	for key, value := range s {
		switch key {
		case "maxLength", "minLength", "minimum", "maximum", "minItems", "maxItems":
			n, ok := value.(float64)
			if !ok || n < 0 || n > 1e9 {
				return fmt.Errorf("invalid numeric schema bound")
			}
		case "type":
			v, ok := value.(string)
			if !ok || !strings.Contains("|object|string|number|integer|boolean|array|null|", "|"+v+"|") {
				return fmt.Errorf("invalid schema type")
			}
		case "properties":
			p, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("invalid properties")
			}
			for name, v := range p {
				if !logicalID.MatchString(name) {
					return fmt.Errorf("invalid property name")
				}
				child, ok := v.(map[string]any)
				if !ok {
					return fmt.Errorf("invalid property")
				}
				if err := validatePublicSchema(child); err != nil {
					return err
				}
			}
		case "required":
			names, ok := value.([]any)
			if !ok {
				return fmt.Errorf("invalid required")
			}
			for _, v := range names {
				n, ok := v.(string)
				if !ok || !logicalID.MatchString(n) {
					return fmt.Errorf("invalid required property")
				}
			}
		case "additionalProperties":
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("additionalProperties must be boolean")
			}
		case "items":
			child, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("invalid items")
			}
			if err := validatePublicSchema(child); err != nil {
				return err
			}
		default:
			return fmt.Errorf("schema keyword %q is not public-safe", key)
		}
	}
	return nil
}

// ValidateAnnouncementJSON rejects unrecognized fields (including nested secrets)
// instead of quietly discarding them while accepting an unsafe advertisement.
func ValidateAnnouncementJSON(raw []byte, a *CapabilityAnnounce) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	delete(fields, "type")
	clean, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(string(clean)))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(a); err != nil {
		return fmt.Errorf("invalid announcement fields: %w", err)
	}
	explicitFalse := string(fields["callable"]) == "false"
	if explicitFalse && a.Kind == "" {
		a.Kind = CapabilityKind(a.CapabilityID)
	}
	return NormalizeAnnouncement(a)
}
