package skillbundle

import (
	"bytes"
	"context"
	"github.com/jiejhong/GooseCab/internal/protocol"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInvokePinnedApprovalAndActiveVersion(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	in, err := s.Stage(bytes.NewReader(bundle(t, 1, "!", false, 0)))
	if err != nil {
		t.Fatal(err)
	}
	v := protocol.CapabilityInvoke{RequestID: "pinned-test", CapabilityID: in.CapabilityID, Payload: map[string]any{"text": " WORLD "}}
	call := func(version int, hash string) protocol.CapabilityResult { return s.InvokePinned(ctx, v, version, hash) }
	if call(1, in.SHA256).Status != "failed" {
		t.Fatal("unapproved executed")
	}
	if err = s.Approve(in.SHA256, in.CapabilityID, 1); err != nil {
		t.Fatal(err)
	}
	for _, p := range []struct {
		v int
		h string
	}{{2, in.SHA256}, {1, strings.Repeat("0", 64)}, {0, in.SHA256}, {1, "../bad"}} {
		if call(p.v, p.h).Status != "failed" {
			t.Fatal("bad pin executed", p)
		}
	}
	r := call(1, in.SHA256)
	if r.Status != "completed" || r.Output.Text != "world!" {
		t.Fatal(r)
	}
	next, err := s.Stage(bytes.NewReader(bundle(t, 2, "?", false, 0)))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Approve(next.SHA256, next.CapabilityID, 2); err != nil {
		t.Fatal(err)
	}
	if call(1, in.SHA256).Status != "failed" {
		t.Fatal("stale pin executed after upgrade")
	}
	if r = call(2, next.SHA256); r.Status != "completed" || r.Output.Text != "world?" {
		t.Fatal(r)
	}
	if err = s.Rollback(in.CapabilityID, 1, in.SHA256); err != nil {
		t.Fatal(err)
	}
	if call(2, next.SHA256).Status != "failed" {
		t.Fatal("stale pin executed after rollback")
	}
	if err = os.WriteFile(filepath.Join(in.Directory, "recipe.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if call(1, in.SHA256).Status != "failed" {
		t.Fatal("tampered recipe executed")
	}
}
func TestPinnedArchiveTamperAndManual(t *testing.T) {
	for _, manual := range []bool{false, true} {
		s := openStore(t)
		in, err := s.Stage(bytes.NewReader(bundle(t, 1, "!", manual, 0)))
		if err != nil {
			t.Fatal(err)
		}
		if err = s.Approve(in.SHA256, in.CapabilityID, 1); err != nil {
			t.Fatal(err)
		}
		if !manual {
			if err = os.WriteFile(filepath.Join(s.root, "staged", in.SHA256, "bundle.zip"), []byte("bad archive"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		v := protocol.CapabilityInvoke{CapabilityID: in.CapabilityID, Payload: map[string]any{"text": "hello"}}
		if s.InvokePinned(context.Background(), v, 1, in.SHA256).Status != "failed" {
			t.Fatal("manual or tampered archive executed")
		}
	}
}

func TestPinnedRevocationAndCancellation(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	in, err := s.Stage(bytes.NewReader(bundle(t, 1, "!", false, 0)))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Approve(in.SHA256, in.CapabilityID, 1); err != nil {
		t.Fatal(err)
	}
	v := protocol.CapabilityInvoke{CapabilityID: in.CapabilityID, Payload: map[string]any{"text": "x"}}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if s.InvokePinned(canceled, v, 1, in.SHA256).Status != "failed" {
		t.Fatal("canceled execution")
	}
	// Simulate an independent local approval administrator revoking this entry.
	if _, err = s.db.Exec("DELETE FROM active WHERE capability=?", in.CapabilityID); err != nil {
		t.Fatal(err)
	}
	if s.CheckPinned(ctx, in.CapabilityID, 1, in.SHA256) == nil || s.InvokePinned(ctx, v, 1, in.SHA256).Status != "failed" {
		t.Fatal("revoked pin executed")
	}
}
