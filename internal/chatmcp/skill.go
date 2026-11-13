package chatmcp

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jiejhong/GooseCab/internal/protocol"
	"github.com/jiejhong/GooseCab/internal/skillbundle"
	"os"
	"path/filepath"
	"regexp"
	"unicode/utf8"
)

type SkillConfig struct {
	Root, Capability, Digest string
	Version                  int
}

// PinnedSkill has no mutable or model-controlled configuration.
type PinnedSkill struct {
	store              *skillbundle.Store
	capability, digest string
	version            int
}
type SkillResult struct {
	Capability string               `json:"capability"`
	Version    int                  `json:"version"`
	Digest     string               `json:"sha256"`
	Output     *protocol.TextOutput `json:"output"`
}

func OpenPinnedSkill(ctx context.Context, c SkillConfig) (*PinnedSkill, error) {
	if c.Root == "" && c.Capability == "" && c.Digest == "" && c.Version == 0 {
		return nil, nil
	}
	if c.Root == "" || len(c.Capability) > 128 || !regexp.MustCompile(`^cap\.skill\.[a-zA-Z0-9_.-]+$`).MatchString(c.Capability) || c.Version < 1 || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(c.Digest) {
		return nil, errors.New("complete valid local skill pin required")
	}
	// Only open an existing store; the MCP adapter cannot create an approval store.
	for _, rel := range []string{"", "staged", "approval.db"} {
		st, err := os.Lstat(filepath.Join(c.Root, rel))
		if err != nil || st.Mode()&os.ModeSymlink != 0 || (rel == "approval.db" && !st.Mode().IsRegular()) || (rel != "approval.db" && !st.IsDir()) {
			return nil, errors.New("existing protected local skill store required")
		}
	}
	st, err := skillbundle.Open(c.Root)
	if err != nil {
		return nil, errors.New("local skill store unavailable")
	}
	if err = st.CheckPinned(ctx, c.Capability, c.Version, c.Digest); err != nil {
		st.Close()
		return nil, errors.New("skill pin is not approved, active and intact")
	}
	return &PinnedSkill{store: st, capability: c.Capability, digest: c.Digest, version: c.Version}, nil
}
func (s *PinnedSkill) Close() error { return s.store.Close() }
func (s *PinnedSkill) Apply(ctx context.Context, text string) (any, error) {
	if !utf8.ValidString(text) || len(text) > 4096 {
		return nil, errors.New("skill text must be valid UTF-8 and at most 4096 bytes")
	}
	r := s.store.InvokePinned(ctx, protocol.CapabilityInvoke{RequestID: uuid.NewString(), CapabilityID: s.capability, Payload: map[string]any{"text": text}}, s.version, s.digest)
	if r.Status != "completed" || r.Output == nil {
		return nil, errors.New("pinned skill unavailable, unapproved, changed or unable to execute")
	}
	return SkillResult{Capability: s.capability, Version: s.version, Digest: s.digest, Output: r.Output}, nil
}
