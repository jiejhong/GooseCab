// Package knowledge stores explicit, agent-local knowledge records. Records are
// inert untrusted data, not instructions, executable skills, or model training.
package knowledge

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	_ "modernc.org/sqlite"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const MaxPackageBytes = 32 << 20
const MaxRecordBytes = 32 << 10
const MaxRecords = 512

var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.:-]{0,127}$`)
var collectionID = regexp.MustCompile(`^[a-z][a-z0-9_]{0,47}$`)

// Callers are exact Gateway actor identities, including namespaced cascade
// bridge IDs. This does not widen the local record/origin identity grammar.
func validCallerID(id string) bool {
	if id == "" || len(id) > 128 || !utf8.ValidString(id) || strings.ContainsAny(id, "*?[]") {
		return false
	}
	for _, r := range id {
		if unicode.IsControl(r) || unicode.IsSpace(r) || !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

type Record struct {
	ID            string   `json:"id"`
	Origin        string   `json:"origin"`
	Version       int      `json:"version"`
	Title         string   `json:"title"`
	Body          string   `json:"body"`
	Tags          []string `json:"tags,omitempty"`
	Source        string   `json:"source"`
	ProvenanceIDs []string `json:"provenance_ids,omitempty"`
	CreatedAt     string   `json:"created_at"`
	UpdatedAt     string   `json:"updated_at"`
}
type Ref struct {
	Origin  string `json:"origin"`
	ID      string `json:"id"`
	Version int    `json:"version"`
	Digest  string `json:"digest"`
}
type Collection struct {
	Name    string   `json:"name"`
	Records []Ref    `json:"records"`
	Callers []string `json:"callers"`
}
type Package struct {
	Format     int      `json:"format"`
	Sender     string   `json:"sender"`
	Recipient  string   `json:"recipient"`
	Collection string   `json:"collection"`
	CreatedAt  string   `json:"created_at"`
	Records    []Record `json:"records"`
}
type Review struct {
	Digest  string  `json:"sha256"`
	Package Package `json:"package"`
}
type Store struct {
	db      *sql.DB
	agentID string
}

func Digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func Reference(r Record) Ref {
	b, _ := json.Marshal(r)
	return Ref{r.Origin, r.ID, r.Version, Digest(b)}
}
func Open(path, agentID string) (*Store, error) {
	if path == "" || path == ":memory:" || strings.HasPrefix(path, "file:") || !identifier.MatchString(agentID) {
		return nil, fmt.Errorf("persistent database path and valid agent ID required")
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = f.Close(); e != nil {
		return nil, e
	}
	db, e := sql.Open("sqlite", path)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	fail := func(e error) (*Store, error) { db.Close(); return nil, e }
	for _, q := range []string{`PRAGMA busy_timeout=5000`, `PRAGMA journal_mode=WAL`, `PRAGMA synchronous=FULL`, `CREATE TABLE IF NOT EXISTS knowledge_owner(id TEXT PRIMARY KEY)`, `CREATE TABLE IF NOT EXISTS knowledge_records(origin TEXT,id TEXT,version INTEGER,digest TEXT NOT NULL,data BLOB NOT NULL,PRIMARY KEY(origin,id,version))`, `CREATE TABLE IF NOT EXISTS knowledge_collections(name TEXT PRIMARY KEY,data BLOB NOT NULL)`, `CREATE TABLE IF NOT EXISTS knowledge_imports(digest TEXT PRIMARY KEY,sender TEXT NOT NULL,imported_at TEXT NOT NULL)`} {
		if _, e = db.Exec(q); e != nil {
			return fail(e)
		}
	}
	if _, e = db.Exec(`INSERT INTO knowledge_owner(id) SELECT ? WHERE NOT EXISTS (SELECT 1 FROM knowledge_owner)`, agentID); e != nil {
		return fail(e)
	}
	var owner string
	if e = db.QueryRow(`SELECT id FROM knowledge_owner`).Scan(&owner); e != nil {
		return fail(e)
	}
	if owner != agentID {
		return fail(fmt.Errorf("database belongs to another agent"))
	}
	return &Store{db, agentID}, nil
}
func (s *Store) Close() error        { return s.db.Close() }
func validText(v string, n int) bool { return utf8.ValidString(v) && len(v) <= n }
func validate(r Record) error {
	b, _ := json.Marshal(r)
	if !identifier.MatchString(r.ID) || !identifier.MatchString(r.Origin) || r.Version < 1 || r.Version > 1000000 || strings.TrimSpace(r.Title) == "" || !validText(r.Title, 256) || (strings.TrimSpace(r.Source) == "" || !validText(r.Source, 512)) || !validText(r.Body, MaxRecordBytes) || len(b) > MaxRecordBytes || len(r.Tags) > 32 || len(r.ProvenanceIDs) > 32 {
		return fmt.Errorf("invalid record identity or bounds")
	}
	for _, v := range append(append([]string{}, r.Tags...), r.ProvenanceIDs...) {
		if !validText(v, 256) {
			return fmt.Errorf("invalid tag or provenance")
		}
	}
	c, e := time.Parse(time.RFC3339Nano, r.CreatedAt)
	if e != nil {
		return fmt.Errorf("invalid created_at")
	}
	u, e := time.Parse(time.RFC3339Nano, r.UpdatedAt)
	if e != nil || u.Before(c) {
		return fmt.Errorf("invalid updated_at")
	}
	return nil
}

// Put only authors records for this store's owner. Imported origins cannot be edited.
func (s *Store) Put(r Record) (Ref, error) {
	if r.Origin == "" {
		r.Origin = s.agentID
	}
	if r.Origin != s.agentID {
		return Ref{}, fmt.Errorf("local writes must use local origin")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if r.CreatedAt == "" {
		r.CreatedAt = now
	}
	if r.UpdatedAt == "" {
		r.UpdatedAt = now
	}
	if e := validate(r); e != nil {
		return Ref{}, e
	}
	tx, e := s.db.Begin()
	if e != nil {
		return Ref{}, e
	}
	defer tx.Rollback()
	if e = insert(tx, r); e != nil {
		return Ref{}, e
	}
	return Reference(r), tx.Commit()
}
func insert(tx *sql.Tx, r Record) error {
	ref := Reference(r)
	var digest string
	e := tx.QueryRow(`SELECT digest FROM knowledge_records WHERE origin=? AND id=? AND version=?`, r.Origin, r.ID, r.Version).Scan(&digest)
	if e == nil {
		if digest != ref.Digest {
			return fmt.Errorf("record version conflict")
		}
		return nil
	}
	if e != sql.ErrNoRows {
		return e
	}
	var latest int
	if e = tx.QueryRow(`SELECT COALESCE(MAX(version),0) FROM knowledge_records WHERE origin=? AND id=?`, r.Origin, r.ID).Scan(&latest); e != nil {
		return e
	}
	if r.Version <= latest {
		return fmt.Errorf("record version rollback rejected")
	}
	b, _ := json.Marshal(r)
	_, e = tx.Exec(`INSERT INTO knowledge_records(origin,id,version,digest,data) VALUES(?,?,?,?,?)`, r.Origin, r.ID, r.Version, ref.Digest, b)
	return e
}
func (s *Store) get(ref Ref) (Record, error) {
	var r Record
	var b []byte
	var d string
	e := s.db.QueryRow(`SELECT data,digest FROM knowledge_records WHERE origin=? AND id=? AND version=?`, ref.Origin, ref.ID, ref.Version).Scan(&b, &d)
	if e != nil {
		return r, e
	}
	if d != ref.Digest {
		return r, fmt.Errorf("selected record digest mismatch")
	}
	e = json.Unmarshal(b, &r)
	return r, e
}

// Search is local and returns current versions, including private record bodies.
func (s *Store) Search(query string) ([]Record, error) {
	if !validText(query, 256) {
		return nil, fmt.Errorf("query too large")
	}
	rows, e := s.db.Query(`SELECT data FROM knowledge_records r WHERE version=(SELECT MAX(version) FROM knowledge_records v WHERE v.origin=r.origin AND v.id=r.id) ORDER BY origin,id`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Record{}
	query = strings.ToLower(query)
	for rows.Next() {
		var b []byte
		if e = rows.Scan(&b); e != nil {
			return nil, e
		}
		var r Record
		if e = json.Unmarshal(b, &r); e != nil {
			return nil, e
		}
		if strings.Contains(strings.ToLower(r.Title+" "+r.Body+" "+strings.Join(r.Tags, " ")), query) {
			out = append(out, r)
			if len(out) >= MaxRecords {
				break
			}
		}
	}
	return out, rows.Err()
}

// Share explicitly pins exact record versions/digests and named callers. No wildcard.
func (s *Store) Share(c Collection) error {
	if !collectionID.MatchString(c.Name) || len(c.Records) < 1 || len(c.Records) > MaxRecords || len(c.Callers) < 1 || len(c.Callers) > 64 {
		return fmt.Errorf("explicit collection, record selection and callers required")
	}
	seen := map[string]bool{}
	for _, ref := range c.Records {
		key := ref.Origin + "/" + ref.ID
		if seen[key] {
			return fmt.Errorf("duplicate selection")
		}
		seen[key] = true
		if _, e := s.get(ref); e != nil {
			return e
		}
	}
	for _, a := range c.Callers {
		if !validCallerID(a) {
			return fmt.Errorf("invalid named caller")
		}
	}
	b, _ := json.Marshal(c)
	_, e := s.db.Exec(`INSERT INTO knowledge_collections(name,data) VALUES(?,?) ON CONFLICT(name) DO UPDATE SET data=excluded.data`, c.Name, b)
	return e
}
func (s *Store) Unshare(name string) error {
	_, e := s.db.Exec(`DELETE FROM knowledge_collections WHERE name=?`, name)
	return e
}
func (s *Store) collection(name string) (Collection, error) {
	var c Collection
	var b []byte
	e := s.db.QueryRow(`SELECT data FROM knowledge_collections WHERE name=?`, name).Scan(&b)
	if e == nil {
		e = json.Unmarshal(b, &c)
	}
	return c, e
}
func allowed(c Collection, who string) bool {
	for _, a := range c.Callers {
		if a == who {
			return true
		}
	}
	return false
}
func (s *Store) Export(name, recipient string) ([]byte, error) {
	c, e := s.collection(name)
	if e != nil {
		return nil, e
	}
	if !allowed(c, recipient) {
		return nil, fmt.Errorf("recipient not authorized for collection")
	}
	p := Package{Format: 1, Sender: s.agentID, Recipient: recipient, Collection: name, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Records: []Record{}}
	for _, ref := range c.Records {
		r, e := s.get(ref)
		if e != nil {
			return nil, e
		}
		p.Records = append(p.Records, r)
	}
	b, e := json.Marshal(p)
	if len(b) > MaxPackageBytes {
		return nil, fmt.Errorf("package exceeds limit")
	}
	return b, e
}
func Inspect(reader io.Reader) (Review, error) {
	var v Review
	b, e := io.ReadAll(io.LimitReader(reader, MaxPackageBytes+1))
	if e != nil {
		return v, e
	}
	if len(b) > MaxPackageBytes {
		return v, fmt.Errorf("package exceeds limit")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e = d.Decode(&v.Package); e != nil {
		return v, e
	}
	if d.Decode(new(any)) != io.EOF {
		return v, fmt.Errorf("trailing data")
	}
	p := v.Package
	if p.Format != 1 || !identifier.MatchString(p.Sender) || !identifier.MatchString(p.Recipient) || !collectionID.MatchString(p.Collection) || len(p.Records) < 1 || len(p.Records) > MaxRecords {
		return v, fmt.Errorf("invalid package")
	}
	if _, e = time.Parse(time.RFC3339Nano, p.CreatedAt); e != nil {
		return v, e
	}
	seen := map[string]bool{}
	for _, r := range p.Records {
		if e = validate(r); e != nil {
			return v, e
		}
		key := r.Origin + "/" + r.ID
		if seen[key] {
			return v, fmt.Errorf("duplicate record identity in package")
		}
		seen[key] = true
	}
	v.Digest = Digest(b)
	return v, nil
}

// Import requires approval of these exact bytes. Package carries no sharing policy.
// A repeated approved import is a no-op, never a second version or implicit reshare.
func (s *Store) Import(b []byte, approvedDigest string) error {
	v, e := Inspect(bytes.NewReader(b))
	if e != nil {
		return e
	}
	if approvedDigest == "" || v.Digest != approvedDigest {
		return fmt.Errorf("explicit matching digest approval required")
	}
	if v.Package.Recipient != s.agentID {
		return fmt.Errorf("package recipient mismatch")
	}
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var found string
	e = tx.QueryRow(`SELECT digest FROM knowledge_imports WHERE digest=?`, v.Digest).Scan(&found)
	if e == nil {
		return nil
	}
	if e != sql.ErrNoRows {
		return e
	}
	for _, r := range v.Package.Records {
		if r.Origin == s.agentID {
			var d string
			e = tx.QueryRow(`SELECT digest FROM knowledge_records WHERE origin=? AND id=? AND version=?`, r.Origin, r.ID, r.Version).Scan(&d)
			if e != nil || d != Reference(r).Digest {
				return fmt.Errorf("remote package cannot author local origin")
			}
		}
		if e = insert(tx, r); e != nil {
			return e
		}
	}
	_, e = tx.Exec(`INSERT INTO knowledge_imports(digest,sender,imported_at) VALUES(?,?,?)`, v.Digest, v.Package.Sender, time.Now().UTC().Format(time.RFC3339Nano))
	if e != nil {
		return e
	}
	return tx.Commit()
}
