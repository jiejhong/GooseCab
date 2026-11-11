package skills

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func recipe(version int, suffix string) Package {
	return Package{Format: 1, Capability: "cap.learned.text.greeting", Version: version, Operations: []Operation{{Op: "trim"}, {Op: "lower"}, {Op: "suffix", Value: suffix}}, Tests: []Case{{Input: " HELLO ", Want: "hello" + suffix}}}
}
func encoded(p Package) []byte { b, _ := json.Marshal(p); return b }
func TestInstallReopenOfflineRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "skills.db")
	s, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	for _, p := range []Package{recipe(1, "!"), recipe(2, "?")} {
		b := encoded(p)
		if e = s.Install(bytes.NewReader(b), Digest(b), p.Capability, p.Version); e != nil {
			t.Fatal(e)
		}
	}
	s.Close()
	s, e = Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	out, e := s.Execute("cap.learned.text.greeting", " WORLD ")
	if e != nil || out != "world?" {
		t.Fatalf("%q %v", out, e)
	}
	if e = s.Rollback("cap.learned.text.greeting", 1); e != nil {
		t.Fatal(e)
	}
	out, e = s.Execute("cap.learned.text.greeting", " AGAIN ")
	if e != nil || out != "again!" {
		t.Fatalf("%q %v", out, e)
	}
}
func TestRejectUnsafePackagesWithoutChangingActive(t *testing.T) {
	s, e := Open(filepath.Join(t.TempDir(), "skills.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	p := recipe(1, "!")
	b := encoded(p)
	if e = s.Install(bytes.NewReader(b), Digest(b), p.Capability, 1); e != nil {
		t.Fatal(e)
	}
	cases := map[string][]byte{}
	p = recipe(2, "?")
	p.Operations[0].Op = "shell"
	cases["shell"] = encoded(p)
	p = recipe(2, "?")
	p.Tests[0].Want = "incorrect"
	cases["evaluation"] = encoded(p)
	p = recipe(2, "?")
	p.Tests = nil
	cases["no evaluation"] = encoded(p)
	p = recipe(2, "?")
	p.Format = 2
	cases["format"] = encoded(p)
	p = recipe(2, "?")
	p.Capability = "../../outside"
	cases["path"] = encoded(p)
	p = recipe(2, strings.Repeat("x", 4096))
	cases["overflow"] = encoded(p)
	cases["oversize"] = []byte(strings.Repeat(" ", MaxPackageBytes+1))
	cases["unknown field"] = []byte(`{"format":1,"command":"rm -rf /"}`)
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			if e = s.Install(bytes.NewReader(b), Digest(b), "cap.learned.text.greeting", 2); e == nil {
				t.Fatal("accepted invalid package")
			}
		})
	}
	b = encoded(recipe(2, "?"))
	if e = s.Install(bytes.NewReader(b), strings.Repeat("0", 64), "cap.learned.text.greeting", 2); e == nil {
		t.Fatal("accepted tampered digest")
	}
	if e = s.Install(bytes.NewReader(b), Digest(b), "cap.learned.text.greeting", 3); e == nil {
		t.Fatal("accepted wrong version")
	}
	b = encoded(recipe(1, "?"))
	if e = s.Install(bytes.NewReader(b), Digest(b), "cap.learned.text.greeting", 1); e == nil {
		t.Fatal("accepted conflicting version")
	}
	out, e := s.Execute("cap.learned.text.greeting", " OK ")
	if e != nil || out != "ok!" {
		t.Fatalf("activation changed: %q %v", out, e)
	}
}
func TestBoundsAndStoredTampering(t *testing.T) {
	s, e := Open(filepath.Join(t.TempDir(), "skills.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	p := recipe(1, "!")
	b := encoded(p)
	if e = s.Install(bytes.NewReader(b), Digest(b), p.Capability, 1); e != nil {
		t.Fatal(e)
	}
	for _, text := range []string{strings.Repeat("a", 4096), string([]byte{0xff})} {
		if _, e = s.Execute(p.Capability, text); e == nil {
			t.Fatal("accepted oversized/invalid output")
		}
	}
	if _, e = s.db.Exec(`UPDATE skill_versions SET package=?`, []byte(`{}`)); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Execute(p.Capability, "hi"); e == nil {
		t.Fatal("executed corrupted local package")
	}
}
func TestDowngradeAndReplaceExpansion(t *testing.T) {
	s, e := Open(filepath.Join(t.TempDir(), "skills.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	p := recipe(2, "!")
	b := encoded(p)
	if e = s.Install(bytes.NewReader(b), Digest(b), p.Capability, 2); e != nil {
		t.Fatal(e)
	}
	p = recipe(1, "?")
	b = encoded(p)
	if e = s.Install(bytes.NewReader(b), Digest(b), p.Capability, 1); e == nil {
		t.Fatal("implicit downgrade accepted")
	}
	p.Operations = []Operation{{Op: "replace", From: "a", Value: strings.Repeat("b", 4096)}}
	if _, e = run(p, strings.Repeat("a", 4096)); e == nil {
		t.Fatal("unbounded replace")
	}
}
