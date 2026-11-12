package chatmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/jiejhong/GooseCab/internal/skillbundle"
	"github.com/jiejhong/GooseCab/internal/skills"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func skillFixture(t *testing.T) (SkillConfig, *skillbundle.Store, skillbundle.Inspection) {
	t.Helper()
	root := t.TempDir()
	src := t.TempDir()
	p := skills.Package{Format: 1, Capability: "cap.skill.greeting", Version: 1, Operations: []skills.Operation{{Op: "trim"}, {Op: "upper"}}, Tests: []skills.Case{{Input: " hi ", Want: "HI"}}}
	raw, _ := json.Marshal(p)
	for n, b := range map[string][]byte{"SKILL.md": []byte("# Greeting"), "recipe.json": raw} {
		if err := os.WriteFile(filepath.Join(src, n), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	manifest := skillbundle.Manifest{Format: 1, Name: "greeting", Version: 1, Description: "greeting", Adapter: skillbundle.Adapter{Kind: "text-recipe", Recipe: "recipe.json"}, Files: []skillbundle.File{{Path: "SKILL.md"}, {Path: "recipe.json"}}}
	var archive bytes.Buffer
	if _, err := skillbundle.Pack(src, manifest, &archive); err != nil {
		t.Fatal(err)
	}
	st, err := skillbundle.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	in, err := st.Stage(&archive)
	if err != nil {
		t.Fatal(err)
	}
	return SkillConfig{Root: root, Capability: in.CapabilityID, Version: 1, Digest: in.SHA256}, st, in
}
func TestSkillDisabledAndPinnedConfiguration(t *testing.T) {
	ctx := context.Background()
	if p, e := OpenPinnedSkill(ctx, SkillConfig{}); p != nil || e != nil {
		t.Fatal("default not disabled")
	}
	s := &Server{Backend: &fakeBackend{}}
	if len(s.toolsList()) != 3 {
		t.Fatal("default tools changed")
	}
	if _, e := s.call(ctx, "skill_apply", json.RawMessage(`{"text":"x"}`)); e == nil {
		t.Fatal("disabled tool called")
	}
	cfg, st, _ := skillFixture(t)
	if _, e := OpenPinnedSkill(ctx, cfg); e == nil {
		t.Fatal("unapproved opened")
	}
	if e := st.Approve(cfg.Digest, cfg.Capability, cfg.Version); e != nil {
		t.Fatal(e)
	}
	for _, bad := range []SkillConfig{
		{Root: cfg.Root}, {Root: cfg.Root, Capability: cfg.Capability, Version: 2, Digest: cfg.Digest},
		{Root: cfg.Root, Capability: cfg.Capability, Version: 1, Digest: strings.Repeat("0", 64)},
		{Root: cfg.Root, Capability: "cap.skill.other", Version: 1, Digest: cfg.Digest},
	} {
		if _, e := OpenPinnedSkill(ctx, bad); e == nil {
			t.Fatal("bad pin opened", bad.Version)
		}
	}
	p, e := OpenPinnedSkill(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	s.Skill = p
	if len(s.toolsList()) != 4 {
		t.Fatal("tool not advertised")
	}
	for _, args := range []string{`{}`, `{"text":"x","root":"elsewhere"}`, `{"text":"x","capability":"other"}`, `{"text":"x","version":2}`, `{"text":"x","sha256":"bad"}`, `{"text":"x","command":"echo"}`} {
		if _, e = s.call(ctx, "skill_apply", json.RawMessage(args)); e == nil {
			t.Fatal("override accepted", args)
		}
	}
	if _, e = p.Apply(ctx, strings.Repeat("x", 4097)); e == nil {
		t.Fatal("oversize accepted")
	}
}
func TestSkillProtocolSuccessMetaAndTamper(t *testing.T) {
	cfg, st, in := skillFixture(t)
	ctx := context.Background()
	if e := st.Approve(cfg.Digest, cfg.Capability, cfg.Version); e != nil {
		t.Fatal(e)
	}
	p, e := OpenPinnedSkill(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	s := &Server{Backend: &fakeBackend{}, Skill: p}
	input := initRPC + "\n" + `{"jsonrpc":"2.0","id":"skill-call","method":"tools/call","params":{"name":"skill_apply","_meta":{"progressToken":42},"arguments":{"text":" hi "}}}` + "\n"
	var out bytes.Buffer
	if e = s.Serve(ctx, strings.NewReader(input), &out); e != nil {
		t.Fatal(e)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	var response struct {
		ID     string
		Result struct {
			IsError bool
			Content []struct{ Text string }
		}
	}
	if e = json.Unmarshal([]byte(lines[1]), &response); e != nil {
		t.Fatal(e)
	}
	if response.ID != "skill-call" || response.Result.IsError {
		t.Fatal(lines[1])
	}
	var r SkillResult
	if e = json.Unmarshal([]byte(response.Result.Content[0].Text), &r); e != nil {
		t.Fatal(e)
	}
	if r.Capability != cfg.Capability || r.Version != 1 || r.Digest != cfg.Digest || r.Output.Text != "HI" {
		t.Fatal(r)
	}
	if strings.Contains(out.String(), cfg.Root) {
		t.Fatal("local root leaked")
	}
	if e = os.WriteFile(filepath.Join(in.Directory, "recipe.json"), []byte("{}"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = p.Apply(ctx, "hello"); e == nil || strings.Contains(e.Error(), cfg.Root) {
		t.Fatal("tamper accepted or path leaked")
	}
}
