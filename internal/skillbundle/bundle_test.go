package skillbundle

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiejhong/GooseCab/internal/protocol"
	"github.com/jiejhong/GooseCab/internal/relay"
	"github.com/jiejhong/GooseCab/internal/skills"
)

func bundle(t *testing.T, version int, suffix string, manual bool, asset int) []byte {
	t.Helper()
	dir := t.TempDir()
	m := Manifest{Format: 1, Name: "greeting", Version: version, Description: "test greeting", Adapter: Adapter{Kind: "text-recipe", Recipe: "recipe.json"}, Files: []File{{Path: "SKILL.md"}, {Path: "recipe.json"}}}
	p := skills.Package{Format: 1, Capability: "cap.skill.greeting", Version: version, Operations: []skills.Operation{{Op: "trim"}, {Op: "lower"}, {Op: "suffix", Value: suffix}}, Tests: []skills.Case{{Input: " HI ", Want: "hi" + suffix}}}
	raw, _ := json.Marshal(p)
	if e := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# Greeting\nReview this recipe."), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, "recipe.json"), raw, 0600); e != nil {
		t.Fatal(e)
	}
	if manual {
		m.Adapter = Adapter{Kind: "manual"}
	}
	if asset > 0 {
		if e := os.WriteFile(filepath.Join(dir, "asset.bin"), bytes.Repeat([]byte{0xa5}, asset), 0600); e != nil {
			t.Fatal(e)
		}
		m.Files = append(m.Files, File{Path: "asset.bin"})
	}
	var b bytes.Buffer
	if _, e := Pack(dir, m, &b); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}
func openStore(t *testing.T) *Store {
	t.Helper()
	s, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func invoke(s *Store) protocol.CapabilityResult {
	return s.Invoke(context.Background(), protocol.CapabilityInvoke{RequestID: "test", CapabilityID: "cap.skill.greeting", Payload: map[string]any{"text": " WORLD "}})
}
func TestRelayStageExplicitApprovalAndOfflineExecution(t *testing.T) {
	b := bundle(t, 1, "!", false, 30<<20)
	server := httptest.NewServer(relay.New(relay.Config{}))
	c := relay.Client{URL: server.URL}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ticket, e := c.Create(ctx, "teacher", "learner", int64(len(b)), skills.Digest(b))
	if e != nil {
		t.Fatal(e)
	}
	send := make(chan error, 1)
	go func() { send <- c.Send(ctx, ticket, bytes.NewReader(b)) }()
	dir := t.TempDir()
	received, e := c.ReceiveFile(ctx, ticket, dir, "greeting.zip")
	if e != nil {
		t.Fatal(e)
	}
	if e = <-send; e != nil {
		t.Fatal(e)
	}
	server.Close() // no teacher or gateway remains
	root := t.TempDir()
	s, e := Open(root)
	if e != nil {
		t.Fatal(e)
	}
	f, e := os.Open(received)
	if e != nil {
		t.Fatal(e)
	}
	in, e := s.Stage(f)
	f.Close()
	if e != nil {
		t.Fatal(e)
	}
	if ids, e := s.CapabilityIDs(); e != nil || len(ids) != 0 {
		t.Fatalf("receipt activated: %v %v", ids, e)
	}
	if invoke(s).Status != "failed" {
		t.Fatal("unapproved executed")
	}
	if e = s.Approve(strings.Repeat("0", 64), in.CapabilityID, 1); e == nil {
		t.Fatal("bad pin accepted")
	}
	if e = s.Approve(in.SHA256, in.CapabilityID, 1); e != nil {
		t.Fatal(e)
	}
	s.Close()
	s, e = Open(root)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	r := invoke(s)
	if r.Status != "completed" || r.Output.Text != "world!" {
		t.Fatalf("offline: %+v", r)
	}
	a, e := s.Announcements()
	if e != nil || len(a) != 1 || !a[0].Callable || a[0].Skill.Digest != in.SHA256 {
		t.Fatalf("announce: %+v %v", a, e)
	}
	if e = os.WriteFile(filepath.Join(in.Directory, "recipe.json"), []byte("{}"), 0600); e != nil {
		t.Fatal(e)
	}
	if invoke(s).Status != "failed" {
		t.Fatal("tampering not detected")
	}
}
func TestManualNotExecutableAndVersionRollback(t *testing.T) {
	s := openStore(t)
	b := bundle(t, 1, "!", false, 0)
	v1, e := s.Stage(bytes.NewReader(b))
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Approve(v1.SHA256, v1.CapabilityID, 1); e != nil {
		t.Fatal(e)
	}
	b = bundle(t, 2, "?", true, 0)
	v2, e := s.Stage(bytes.NewReader(b))
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Approve(v2.SHA256, v2.CapabilityID, 2); e != nil {
		t.Fatal(e)
	}
	if ids, e := s.CapabilityIDs(); e != nil || len(ids) != 0 {
		t.Fatal(ids, e)
	}
	if invoke(s).Status != "failed" {
		t.Fatal("manual executed")
	}
	a, e := s.Announcements()
	if e != nil || len(a) != 1 || a[0].Callable {
		t.Fatal(a, e)
	}
	if e = s.Approve(v1.SHA256, v1.CapabilityID, 1); e == nil {
		t.Fatal("silent downgrade")
	}
	if e = s.Rollback(v1.CapabilityID, 1, v2.SHA256); e == nil {
		t.Fatal("wrong rollback pin")
	}
	if e = s.Rollback(v1.CapabilityID, 1, v1.SHA256); e != nil {
		t.Fatal(e)
	}
	if invoke(s).Status != "completed" {
		t.Fatal("rollback failed")
	}
	b = bundle(t, 1, "x", false, 0)
	other, e := s.Stage(bytes.NewReader(b))
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Approve(other.SHA256, other.CapabilityID, 1); e == nil {
		t.Fatal("version conflict accepted")
	}
}
func archive(t *testing.T, files map[string][]byte, extra string, symlink bool) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for name, v := range files {
		f, e := z.Create(name)
		if e != nil {
			t.Fatal(e)
		}
		f.Write(v)
	}
	if extra != "" {
		h := &zip.FileHeader{Name: extra, Method: zip.Deflate}
		if symlink {
			h.SetMode(os.ModeSymlink | 0777)
		}
		f, e := z.CreateHeader(h)
		if e != nil {
			t.Fatal(e)
		}
		f.Write([]byte("x"))
	}
	if e := z.Close(); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}
func TestRejectMaliciousArchives(t *testing.T) {
	good := bundle(t, 1, "!", false, 0)
	_, files, e := validate(good)
	if e != nil {
		t.Fatal(e)
	}
	cases := map[string][]byte{}
	for _, p := range []string{"../outside", "/absolute", "a/../../b", "C:/escape", "a\\b", "SKILL.md"} {
		cases[p] = archive(t, files, p, false)
	}
	cases["symlink"] = archive(t, files, "link", true)
	for name, mutate := range map[string]func(map[string][]byte){
		"unknown approval": func(m map[string][]byte) {
			m["manifest.json"] = bytes.Replace(m["manifest.json"], []byte(`"format":1`), []byte(`"format":1,"approved":true`), 1)
		},
		"duplicate JSON": func(m map[string][]byte) {
			m["manifest.json"] = bytes.Replace(m["manifest.json"], []byte(`"format":1`), []byte(`"format":1,"format":1`), 1)
		},
		"undeclared file": func(m map[string][]byte) { m["approval.db"] = []byte("forged") },
		"tampered file":   func(m map[string][]byte) { m["recipe.json"] = []byte("{}") },
		"missing skill":   func(m map[string][]byte) { delete(m, "SKILL.md") },
		"zip bomb":        func(m map[string][]byte) { m["bomb"] = bytes.Repeat([]byte("x"), MaxBundleBytes+1) },
	} {
		copy := map[string][]byte{}
		for n, v := range files {
			copy[n] = append([]byte(nil), v...)
		}
		mutate(copy)
		cases[name] = archive(t, copy, "", false)
	}
	s := openStore(t)
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			if _, e := s.Stage(bytes.NewReader(b)); e == nil {
				t.Fatal("unsafe bundle accepted")
			}
		})
	}
	if _, e = s.Stage(bytes.NewReader(bytes.Repeat([]byte("x"), MaxBundleBytes+1))); e == nil {
		t.Fatal("oversize compressed archive")
	}
}
func TestRejectLocalSymlinks(t *testing.T) {
	s := openStore(t)
	b := bundle(t, 1, "!", false, 0)
	in, e := s.Stage(bytes.NewReader(b))
	if e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(in.Directory, "SKILL.md")
	if e = os.Remove(p); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink("../bundle.zip", p); e != nil {
		t.Fatal(e)
	}
	if e = s.Approve(in.SHA256, in.CapabilityID, 1); e == nil {
		t.Fatal("symlink approved")
	}
}

func TestRemoteResultDoesNotLeakLocalPaths(t *testing.T) {
	s := openStore(t)
	in, e := s.Stage(bytes.NewReader(bundle(t, 1, "!", false, 0)))
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Approve(in.SHA256, in.CapabilityID, 1); e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(filepath.Join(in.Directory, "SKILL.md")); e != nil {
		t.Fatal(e)
	}
	r := invoke(s)
	if r.Status != "failed" || strings.Contains(r.Error, s.root) || strings.Contains(r.Error, "SKILL.md") || strings.Contains(r.Error, in.SHA256) {
		t.Fatalf("leaked local details: %+v", r)
	}
	if _, e = s.Inspect(in.SHA256); e == nil {
		t.Fatal("local inspection must report failure")
	}
}
