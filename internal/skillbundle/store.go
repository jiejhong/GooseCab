package skillbundle

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/jiejhong/GooseCab/internal/protocol"
	"github.com/jiejhong/GooseCab/internal/skills"
	_ "modernc.org/sqlite"
)

type Store struct {
	root string
	db   *sql.DB
	mu   sync.Mutex
}

// Open requires a protected agent-local root. Its ancestors must be trusted and
// must not be writable by peers. Received bundles cannot contain local state.
func Open(root string) (*Store, error) {
	if root == "" {
		return nil, fmt.Errorf("local root required")
	}
	root, e := filepath.Abs(root)
	if e != nil {
		return nil, e
	}
	if e = os.MkdirAll(root, 0700); e != nil {
		return nil, e
	}
	st, e := os.Lstat(root)
	if e != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("trusted directory required")
	}
	stage := filepath.Join(root, "staged")
	if e = os.MkdirAll(stage, 0700); e != nil {
		return nil, e
	}
	st, e = os.Lstat(stage)
	if e != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("unsafe staging directory")
	}
	dbPath := filepath.Join(root, "approval.db")
	if st, e = os.Lstat(dbPath); e == nil && (!st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0) {
		return nil, fmt.Errorf("unsafe local database")
	}
	f, e := os.OpenFile(dbPath, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	f.Close()
	db, e := sql.Open("sqlite", dbPath)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	for _, q := range []string{`PRAGMA busy_timeout=5000`, `PRAGMA journal_mode=WAL`, `PRAGMA synchronous=FULL`, `CREATE TABLE IF NOT EXISTS approved(capability TEXT NOT NULL,version INTEGER NOT NULL,digest TEXT NOT NULL,PRIMARY KEY(capability,version))`, `CREATE TABLE IF NOT EXISTS active(capability TEXT PRIMARY KEY,version INTEGER NOT NULL)`} {
		if _, e = db.Exec(q); e != nil {
			db.Close()
			return nil, e
		}
	}
	return &Store{root: root, db: db}, nil
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Stage(r io.Reader) (Inspection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, e := io.ReadAll(io.LimitReader(r, MaxBundleBytes+1))
	if e != nil {
		return Inspection{}, e
	}
	in, files, e := validate(b)
	if e != nil {
		return in, e
	}
	dst := filepath.Join(s.root, "staged", in.SHA256)
	if _, e = os.Lstat(dst); e == nil {
		return s.Inspect(in.SHA256)
	} else if !os.IsNotExist(e) {
		return in, e
	}
	tmp, e := os.MkdirTemp(filepath.Join(s.root, "staged"), ".stage-")
	if e != nil {
		return in, e
	}
	defer os.RemoveAll(tmp)
	if e = writeSynced(filepath.Join(tmp, "bundle.zip"), b); e != nil {
		return in, e
	}
	content := filepath.Join(tmp, "files")
	if e = os.Mkdir(content, 0700); e != nil {
		return in, e
	}
	for n, b := range files {
		p := filepath.Join(content, filepath.FromSlash(n))
		if e = os.MkdirAll(filepath.Dir(p), 0700); e != nil {
			return in, e
		}
		if e = writeSynced(p, b); e != nil {
			return in, e
		}
	}
	if e = os.Rename(tmp, dst); e != nil {
		return in, e
	}
	in.Directory = filepath.Join(dst, "files")
	return in, nil
}
func writeSynced(p string, b []byte) error {
	f, e := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	if _, e = f.Write(b); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	return f.Close()
}

// Inspect rechecks both archived bytes and actual staged files before approval
// or invocation. Package-provided approvals and undeclared files are never used.
func (s *Store) Inspect(hash string) (Inspection, error) {
	if !hashPattern.MatchString(hash) {
		return Inspection{}, fmt.Errorf("SHA256 required")
	}
	rel := "staged/" + hash + "/bundle.zip"
	if e := regularPath(s.root, rel); e != nil {
		return Inspection{}, e
	}
	f, e := os.Open(filepath.Join(s.root, filepath.FromSlash(rel)))
	if e != nil {
		return Inspection{}, e
	}
	b, e := io.ReadAll(io.LimitReader(f, MaxBundleBytes+1))
	f.Close()
	if e != nil {
		return Inspection{}, e
	}
	in, files, e := validate(b)
	if e != nil {
		return in, e
	}
	if in.SHA256 != hash {
		return in, fmt.Errorf("staged bundle digest mismatch")
	}
	in.Directory = filepath.Join(s.root, "staged", hash, "files")
	for n, want := range files {
		rel := "staged/" + hash + "/files/" + n
		if e = regularPath(s.root, rel); e != nil {
			return in, e
		}
		f, e := os.Open(filepath.Join(in.Directory, filepath.FromSlash(n)))
		if e != nil {
			return in, e
		}
		b, e := io.ReadAll(io.LimitReader(f, int64(len(want))+1))
		f.Close()
		if e != nil {
			return in, e
		}
		if !bytes.Equal(b, want) {
			return in, fmt.Errorf("staged file changed: %s", n)
		}
	}
	return in, nil
}

// Approve is an explicit local action with an independently reviewed digest,
// capability and version. No received field can activate a package by itself.
func (s *Store) Approve(hash, capability string, version int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	in, e := s.Inspect(hash)
	if e != nil {
		return e
	}
	if in.CapabilityID != capability || in.Manifest.Version != version {
		return fmt.Errorf("approval identity mismatch")
	}
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var prior string
	e = tx.QueryRow(`SELECT digest FROM approved WHERE capability=? AND version=?`, capability, version).Scan(&prior)
	if e != nil && e != sql.ErrNoRows {
		return e
	}
	if e == nil && prior != hash {
		return fmt.Errorf("immutable version conflict")
	}
	var active int
	e = tx.QueryRow(`SELECT version FROM active WHERE capability=?`, capability).Scan(&active)
	if e != nil && e != sql.ErrNoRows {
		return e
	}
	if active > version {
		return fmt.Errorf("explicit rollback required")
	}
	if _, e = tx.Exec(`INSERT INTO approved VALUES(?,?,?) ON CONFLICT DO NOTHING`, capability, version, hash); e != nil {
		return e
	}
	if _, e = tx.Exec(`INSERT INTO active VALUES(?,?) ON CONFLICT(capability) DO UPDATE SET version=excluded.version`, capability, version); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) Rollback(capability string, version int, hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var pinned string
	if e := s.db.QueryRow(`SELECT digest FROM approved WHERE capability=? AND version=?`, capability, version).Scan(&pinned); e != nil {
		return e
	}
	if pinned != hash {
		return fmt.Errorf("rollback digest does not match previously approved version")
	}
	in, e := s.Inspect(hash)
	if e != nil {
		return e
	}
	if in.CapabilityID != capability || in.Manifest.Version != version {
		return fmt.Errorf("rollback identity mismatch")
	}
	_, e = s.db.Exec(`UPDATE active SET version=? WHERE capability=?`, version, capability)
	return e
}
func (s *Store) active() ([]Inspection, error) {
	rows, e := s.db.Query(`SELECT p.digest FROM approved p JOIN active a ON p.capability=a.capability AND p.version=a.version ORDER BY a.capability`)
	if e != nil {
		return nil, e
	}
	var hashes []string
	for rows.Next() {
		var h string
		if e = rows.Scan(&h); e != nil {
			rows.Close()
			return nil, e
		}
		hashes = append(hashes, h)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	var out []Inspection
	for _, h := range hashes {
		in, e := s.Inspect(h)
		if e != nil {
			return nil, e
		}
		out = append(out, in)
	}
	return out, nil
}
func (s *Store) CapabilityIDs() ([]string, error) {
	all, e := s.active()
	if e != nil {
		return nil, e
	}
	var ids []string
	for _, in := range all {
		if in.Executable {
			ids = append(ids, in.CapabilityID)
		}
	}
	return ids, nil
}
func (s *Store) Announcements() ([]protocol.CapabilityAnnounce, error) {
	all, e := s.active()
	if e != nil {
		return nil, e
	}
	var out []protocol.CapabilityAnnounce
	for _, in := range all {
		m := in.Manifest
		a := protocol.CapabilityAnnounce{CapabilityID: in.CapabilityID, Name: m.Name, Version: strconv.Itoa(m.Version), Kind: "skill", Description: m.Description, Callable: in.Executable, Skill: &protocol.SkillDescriptor{Digest: in.SHA256, Version: strconv.Itoa(m.Version), EntrypointKind: m.Adapter.Kind}}
		if in.Executable {
			a.InputSchema = json.RawMessage(`{"type":"object","properties":{"text":{"type":"string","maxLength":4096}},"required":["text"],"additionalProperties":false}`)
		}
		out = append(out, a)
	}
	return out, nil
}
func (s *Store) Invoke(ctx context.Context, v protocol.CapabilityInvoke) protocol.CapabilityResult {
	r := protocol.CapabilityResult{RequestID: v.RequestID, Status: "failed"}
	if e := ctx.Err(); e != nil {
		r.Error = "Skill invocation canceled"
		return r
	}
	var hash string
	if e := s.db.QueryRowContext(ctx, `SELECT p.digest FROM approved p JOIN active a ON p.capability=a.capability AND p.version=a.version WHERE a.capability=?`, v.CapabilityID).Scan(&hash); e != nil {
		r.Error = "Skill not locally approved"
		return r
	}
	in, e := s.Inspect(hash)
	if e != nil {
		r.Error = "Skill execution failed; inspect the local package"
		return r
	}
	return invokeInspected(ctx, v, in)
}

func invokeInspected(ctx context.Context, v protocol.CapabilityInvoke, in Inspection) protocol.CapabilityResult {
	r := protocol.CapabilityResult{RequestID: v.RequestID, Status: "failed"}
	if ctx.Err() != nil {
		r.Error = "Skill invocation canceled"
		return r
	}
	if !in.Executable || in.CapabilityID != v.CapabilityID {
		r.Error = "Skill is manual, not executable"
		return r
	}
	raw, e := json.Marshal(v.Payload)
	if e != nil {
		r.Error = "Invalid payload"
		return r
	}
	var input struct {
		Text *string `json:"text"`
	}
	if e = strictJSON(raw, &input); e != nil || input.Text == nil {
		r.Error = "Expected only a text string"
		return r
	}
	f, e := os.Open(filepath.Join(in.Directory, filepath.FromSlash(in.Manifest.Adapter.Recipe)))
	if e != nil {
		r.Error = "Skill execution failed; inspect the local package"
		return r
	}
	defer f.Close()
	recipe, e := io.ReadAll(io.LimitReader(f, skills.MaxPackageBytes+1))
	if e != nil {
		r.Error = "Skill execution failed; inspect the local package"
		return r
	}
	var expected string
	for _, entry := range in.Manifest.Files {
		if entry.Path == in.Manifest.Adapter.Recipe {
			expected = entry.SHA256
		}
	}
	if skills.Digest(recipe) != expected {
		r.Error = "Recipe changed after inspection"
		return r
	}
	text, e := skills.ExecutePackage(bytes.NewReader(recipe), *input.Text)
	if e != nil {
		r.Error = "Skill execution failed; inspect the local package"
		return r
	}
	r.Status = "completed"
	r.ResultAvailable = true
	r.Output = &protocol.TextOutput{Text: text, MediaType: "text/plain"}
	return r
}
