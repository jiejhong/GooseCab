package skillbundle

import (
	"context"
	"fmt"
	"github.com/jiejhong/GooseCab/internal/protocol"
)

// inspectPinned reads the active approval exactly once, then verifies the pinned
// archive and extracted files. It never resolves the active version again.
func (s *Store) inspectPinned(ctx context.Context, capability string, version int, digest string) (Inspection, error) {
	if version < 1 || !hashPattern.MatchString(digest) {
		return Inspection{}, fmt.Errorf("invalid skill pin")
	}
	var activeDigest string
	var activeVersion int
	err := s.db.QueryRowContext(ctx, `SELECT p.digest,p.version FROM approved p JOIN active a ON p.capability=a.capability AND p.version=a.version WHERE a.capability=?`, capability).Scan(&activeDigest, &activeVersion)
	if err != nil || activeDigest != digest || activeVersion != version {
		return Inspection{}, fmt.Errorf("skill is not active with the approved pin")
	}
	in, err := s.Inspect(digest)
	if err != nil || !in.Executable || in.CapabilityID != capability || in.Manifest.Version != version {
		return Inspection{}, fmt.Errorf("pinned skill integrity or identity check failed")
	}
	return in, nil
}

// CheckPinned validates an active, locally approved executable package without executing it.
func (s *Store) CheckPinned(ctx context.Context, capability string, version int, digest string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.inspectPinned(ctx, capability, version, digest)
	return err
}

// InvokePinned executes only the exact active approval selected by this call.
// It does not re-resolve active state after the pin comparison. Concurrent changes
// in another process take effect on the next call, not by swapping this recipe.
func (s *Store) InvokePinned(ctx context.Context, v protocol.CapabilityInvoke, version int, digest string) protocol.CapabilityResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	in, err := s.inspectPinned(ctx, v.CapabilityID, version, digest)
	if err != nil {
		return protocol.CapabilityResult{RequestID: v.RequestID, Status: "failed", Error: "Pinned skill unavailable, unapproved or changed"}
	}
	return invokeInspected(ctx, v, in)
}
