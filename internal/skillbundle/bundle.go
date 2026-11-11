// Package skillbundle stages reviewable Skill files on agents. Receiving bytes
// never grants execution permission; only a local digest-pinned approval does.
package skillbundle

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/jiejhong/GooseCab/internal/skills"
)

const MaxBundleBytes = 32 << 20
const MaxManifestBytes = 64 << 10
const MaxFiles = 256

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,47}$`)
var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type File struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type Adapter struct {
	Kind   string `json:"kind"`
	Recipe string `json:"recipe,omitempty"`
}
type Manifest struct {
	Format      int     `json:"format"`
	Name        string  `json:"name"`
	Version     int     `json:"version"`
	Description string  `json:"description"`
	Adapter     Adapter `json:"adapter"`
	Files       []File  `json:"files"`
}
type Inspection struct {
	SHA256       string   `json:"sha256"`
	Manifest     Manifest `json:"manifest"`
	CapabilityID string   `json:"capability_id"`
	Executable   bool     `json:"executable"`
	Directory    string   `json:"directory,omitempty"`
}

func validPath(s string) bool {
	for _, r := range s {
		if r < 32 || r == 127 {
			return false
		}
	}
	if s == "" || len(s) > 240 || path.Clean(s) != s || strings.ContainsAny(s, "\\:\x00") || strings.HasPrefix(s, "/") {
		return false
	}
	for _, p := range strings.Split(s, "/") {
		if p == ".." || p == "." || p == "" {
			return false
		}
	}
	return true
}

// strictJSON also rejects duplicate object keys, which encoding/json normally accepts.
func strictJSON(b []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	var walk func() error
	walk = func() error {
		t, e := d.Token()
		if e != nil {
			return e
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return e
				}
				s, ok := k.(string)
				if !ok || seen[s] {
					return fmt.Errorf("duplicate/invalid JSON key")
				}
				seen[s] = true
				if e = walk(); e != nil {
					return e
				}
			}
		case '[':
			for d.More() {
				if e := walk(); e != nil {
					return e
				}
			}
		default:
			return fmt.Errorf("invalid JSON delimiter")
		}
		_, e = d.Token()
		return e
	}
	if e := walk(); e != nil {
		return e
	}
	if _, e := d.Token(); e != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	d = json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	return d.Decode(out)
}
func validate(b []byte) (Inspection, map[string][]byte, error) {
	var in Inspection
	if len(b) == 0 || len(b) > MaxBundleBytes {
		return in, nil, fmt.Errorf("bundle size must be 1..%d", MaxBundleBytes)
	}
	z, e := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if e != nil {
		return in, nil, e
	}
	if len(z.File) < 2 || len(z.File) > MaxFiles+1 {
		return in, nil, fmt.Errorf("invalid file count")
	}
	files := map[string][]byte{}
	var total uint64
	for _, f := range z.File {
		if !validPath(f.Name) || !f.Mode().IsRegular() {
			return in, nil, fmt.Errorf("unsafe/non-regular archive path: %q", f.Name)
		}
		if _, ok := files[f.Name]; ok {
			return in, nil, fmt.Errorf("duplicate archive path")
		}
		limit := uint64(MaxBundleBytes)
		if f.Name == "manifest.json" {
			limit = MaxManifestBytes
		}
		if f.UncompressedSize64 > limit || f.UncompressedSize64 > MaxBundleBytes-total {
			return in, nil, fmt.Errorf("expanded size exceeds bounds")
		}
		total += f.UncompressedSize64
		r, e := f.Open()
		if e != nil {
			return in, nil, e
		}
		v, e := io.ReadAll(io.LimitReader(r, int64(limit)+1))
		ce := r.Close()
		if e != nil {
			return in, nil, e
		}
		if ce != nil {
			return in, nil, ce
		}
		if uint64(len(v)) != f.UncompressedSize64 {
			return in, nil, fmt.Errorf("entry size mismatch")
		}
		files[f.Name] = v
	}
	raw, ok := files["manifest.json"]
	if !ok {
		return in, nil, fmt.Errorf("manifest.json required")
	}
	if e = strictJSON(raw, &in.Manifest); e != nil {
		return in, nil, e
	}
	m := in.Manifest
	if m.Format != 1 || !namePattern.MatchString(m.Name) || m.Version < 1 || m.Version > 1000000 || len(m.Description) == 0 || len(m.Description) > 1024 || !utf8.ValidString(m.Description) || len(m.Files) < 1 || len(m.Files) > MaxFiles {
		return in, nil, fmt.Errorf("invalid manifest identity/bounds")
	}
	if len(m.Files) != len(files)-1 {
		return in, nil, fmt.Errorf("undeclared or missing files")
	}
	seen := map[string]bool{}
	for _, f := range m.Files {
		if !validPath(f.Path) || f.Path == "manifest.json" || seen[f.Path] || f.Size < 0 || f.Size > MaxBundleBytes || !hashPattern.MatchString(f.SHA256) {
			return in, nil, fmt.Errorf("invalid manifest file")
		}
		seen[f.Path] = true
		v, ok := files[f.Path]
		if !ok || int64(len(v)) != f.Size || skills.Digest(v) != f.SHA256 {
			return in, nil, fmt.Errorf("file size/hash mismatch: %s", f.Path)
		}
	}
	if !seen["SKILL.md"] || !utf8.Valid(files["SKILL.md"]) || len(files["SKILL.md"]) == 0 || len(files["SKILL.md"]) > MaxManifestBytes {
		return in, nil, fmt.Errorf("nonempty SKILL.md <=64KiB required")
	}
	in.CapabilityID = "cap.skill." + m.Name
	in.SHA256 = skills.Digest(b)
	switch m.Adapter.Kind {
	case "manual":
		if m.Adapter.Recipe != "" {
			return in, nil, fmt.Errorf("manual adapter cannot declare recipe")
		}
	case "text-recipe":
		if !seen[m.Adapter.Recipe] {
			return in, nil, fmt.Errorf("declared recipe missing")
		}
		p, _, e := skills.Inspect(bytes.NewReader(files[m.Adapter.Recipe]))
		if e != nil {
			return in, nil, e
		}
		if p.Capability != in.CapabilityID || p.Version != m.Version {
			return in, nil, fmt.Errorf("recipe identity/version mismatch")
		}
		in.Executable = true
	default:
		return in, nil, fmt.Errorf("unsupported adapter; scripts and dependency installation are not supported")
	}
	return in, files, nil
}

// Pack reads only explicitly listed regular local files. The caller supplies
// identity and adapter; file sizes and hashes are computed, not trusted.
func Pack(dir string, m Manifest, w io.Writer) (Inspection, error) {
	var empty Inspection
	if len(m.Files) < 1 || len(m.Files) > MaxFiles {
		return empty, fmt.Errorf("invalid file count")
	}
	data := map[string][]byte{}
	total := 0
	for i, f := range m.Files {
		if !validPath(f.Path) || f.Path == "manifest.json" {
			return empty, fmt.Errorf("unsafe path")
		}
		if _, ok := data[f.Path]; ok {
			return empty, fmt.Errorf("duplicate path")
		}
		p := filepath.Join(dir, filepath.FromSlash(f.Path))
		if e := regularPath(dir, f.Path); e != nil {
			return empty, e
		}
		r, e := os.Open(p)
		if e != nil {
			return empty, e
		}
		b, e := io.ReadAll(io.LimitReader(r, int64(MaxBundleBytes-total)+1))
		r.Close()
		if e != nil {
			return empty, e
		}
		total += len(b)
		if total > MaxBundleBytes {
			return empty, fmt.Errorf("bundle too large")
		}
		m.Files[i].Size = int64(len(b))
		m.Files[i].SHA256 = skills.Digest(b)
		data[f.Path] = b
	}
	raw, e := json.Marshal(m)
	if e != nil {
		return empty, e
	}
	data["manifest.json"] = raw
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	names := make([]string, 0, len(data))
	for n := range data {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		f, e := zw.Create(n)
		if e != nil {
			return empty, e
		}
		if _, e = f.Write(data[n]); e != nil {
			return empty, e
		}
	}
	if e = zw.Close(); e != nil {
		return empty, e
	}
	in, _, e := validate(buf.Bytes())
	if e != nil {
		return empty, e
	}
	_, e = w.Write(buf.Bytes())
	return in, e
}
func regularPath(root, rel string) error {
	p := root
	st, e := os.Lstat(p)
	if e != nil {
		return e
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("trusted directory required")
	}
	parts := strings.Split(rel, "/")
	for i, c := range parts {
		p = filepath.Join(p, c)
		st, e = os.Lstat(p)
		if e != nil {
			return e
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink rejected")
		}
		if i < len(parts)-1 {
			if !st.IsDir() {
				return fmt.Errorf("directory required")
			}
		} else if !st.Mode().IsRegular() {
			return fmt.Errorf("regular file required")
		}
	}
	return nil
}

// ReadManifest strictly parses a bounded local manifest template.
func ReadManifest(r io.Reader) (Manifest, error) {
	var m Manifest
	b, e := io.ReadAll(io.LimitReader(r, MaxManifestBytes+1))
	if e != nil {
		return m, e
	}
	if len(b) > MaxManifestBytes {
		return m, fmt.Errorf("manifest too large")
	}
	e = strictJSON(b, &m)
	return m, e
}
