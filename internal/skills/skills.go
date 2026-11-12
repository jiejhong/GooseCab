// Package skills installs bounded declarative text recipes on the agent only.
// Packages cannot execute code, access files, launch processes, or use networks.
package skills

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
	"unicode/utf8"
)

const MaxPackageBytes = 65536
const MaxTextBytes = 4096

var capabilityPattern = regexp.MustCompile(`^cap\.(learned\.text|skill)\.[a-z][a-z0-9_]{0,47}$`)

func ValidCapability(id string) bool { return capabilityPattern.MatchString(id) }

type Operation struct {
	Op    string `json:"op"`
	Value string `json:"value,omitempty"`
	From  string `json:"from,omitempty"`
}
type Case struct {
	Input string `json:"input"`
	Want  string `json:"want"`
}
type Package struct {
	Format     int         `json:"format"`
	Capability string      `json:"capability"`
	Version    int         `json:"version"`
	Operations []Operation `json:"operations"`
	Tests      []Case      `json:"tests"`
}
type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" || path == ":memory:" || strings.HasPrefix(path, "file:") {
		return nil, fmt.Errorf("persistent local skill database required")
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
	for _, q := range []string{`PRAGMA busy_timeout=5000`, `PRAGMA journal_mode=WAL`, `PRAGMA synchronous=FULL`, `CREATE TABLE IF NOT EXISTS skill_versions(capability TEXT NOT NULL,version INTEGER NOT NULL,digest TEXT NOT NULL,package BLOB NOT NULL,PRIMARY KEY(capability,version))`, `CREATE TABLE IF NOT EXISTS skill_active(capability TEXT PRIMARY KEY,version INTEGER NOT NULL)`} {
		if _, e = db.Exec(q); e != nil {
			db.Close()
			return nil, e
		}
	}
	return &Store{db}, nil
}
func (s *Store) Close() error { return s.db.Close() }
func Digest(b []byte) string  { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func textOK(t string) bool    { return utf8.ValidString(t) && len(t) <= MaxTextBytes }
func decode(b []byte) (Package, error) {
	var p Package
	if len(b) > MaxPackageBytes {
		return p, fmt.Errorf("package exceeds size limit")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e := d.Decode(&p); e != nil {
		return p, e
	}
	if d.Decode(new(any)) != io.EOF {
		return p, fmt.Errorf("trailing package data")
	}
	if p.Format != 1 || !ValidCapability(p.Capability) || p.Version < 1 || p.Version > 1000000 || len(p.Operations) < 1 || len(p.Operations) > 16 || len(p.Tests) < 1 || len(p.Tests) > 16 {
		return p, fmt.Errorf("invalid package identity, version or bounds")
	}
	for _, op := range p.Operations {
		if !textOK(op.Value) || !textOK(op.From) {
			return p, fmt.Errorf("invalid operation text")
		}
		switch op.Op {
		case "upper", "lower", "trim":
			if op.Value != "" || op.From != "" {
				return p, fmt.Errorf("unexpected operation arguments")
			}
		case "prefix", "suffix":
			if op.From != "" {
				return p, fmt.Errorf("unexpected from argument")
			}
		case "replace":
			if op.From == "" {
				return p, fmt.Errorf("replace requires nonempty from")
			}
		default:
			return p, fmt.Errorf("operation not allowed: %s", op.Op)
		}
	}
	for _, test := range p.Tests {
		if !textOK(test.Input) || !textOK(test.Want) {
			return p, fmt.Errorf("invalid evaluation text")
		}
		out, e := run(p, test.Input)
		if e != nil || out != test.Want {
			return p, fmt.Errorf("evaluation failed")
		}
	}
	return p, nil
}
func run(p Package, t string) (string, error) {
	if !textOK(t) {
		return "", fmt.Errorf("expected UTF-8 text of at most 4096 bytes")
	}
	for _, op := range p.Operations {
		switch op.Op {
		case "upper":
			t = strings.ToUpper(t)
		case "lower":
			t = strings.ToLower(t)
		case "trim":
			t = strings.TrimSpace(t)
		case "prefix":
			t = op.Value + t
		case "suffix":
			t += op.Value
		case "replace":
			// Reject expansion before allocation, including worst-case many matches.
			n := strings.Count(t, op.From)
			if len(t)+n*(len(op.Value)-len(op.From)) > MaxTextBytes {
				return "", fmt.Errorf("output exceeds 4096 bytes")
			}
			t = strings.ReplaceAll(t, op.From, op.Value)
		default:
			return "", fmt.Errorf("unsupported operation")
		}
		if !textOK(t) {
			return "", fmt.Errorf("output exceeds 4096 bytes")
		}
	}
	return t, nil
}

// Install verifies the externally pinned identity/hash and all test cases before
// atomically storing bytes and activating the version. Reusing a version with
// different bytes or silently downgrading is rejected. Tests are examples, not
// proof of semantic correctness or trust in the teacher.
func (s *Store) Install(r io.Reader, hash, capability string, version int) error {
	b, e := io.ReadAll(io.LimitReader(r, MaxPackageBytes+1))
	if e != nil {
		return e
	}
	if len(hash) != 64 || Digest(b) != hash {
		return fmt.Errorf("package digest mismatch")
	}
	p, e := decode(b)
	if e != nil {
		return e
	}
	if p.Capability != capability || p.Version != version {
		return fmt.Errorf("package identity/version mismatch")
	}
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var old string
	e = tx.QueryRow(`SELECT digest FROM skill_versions WHERE capability=? AND version=?`, capability, version).Scan(&old)
	if e != nil && e != sql.ErrNoRows {
		return e
	}
	if e == nil && old != hash {
		return fmt.Errorf("immutable version conflict")
	}
	var active int
	e = tx.QueryRow(`SELECT version FROM skill_active WHERE capability=?`, capability).Scan(&active)
	if e != nil && e != sql.ErrNoRows {
		return e
	}
	if active > version {
		return fmt.Errorf("explicit rollback required")
	}
	if _, e = tx.Exec(`INSERT INTO skill_versions(capability,version,digest,package) VALUES(?,?,?,?) ON CONFLICT DO NOTHING`, capability, version, hash, b); e != nil {
		return e
	}
	if _, e = tx.Exec(`INSERT INTO skill_active(capability,version) VALUES(?,?) ON CONFLICT(capability) DO UPDATE SET version=excluded.version`, capability, version); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) load(capability string, version int) (Package, error) {
	var b []byte
	var digest string
	var e error
	if version == 0 {
		e = s.db.QueryRow(`SELECT p.package,p.digest FROM skill_versions p JOIN skill_active a ON p.capability=a.capability AND p.version=a.version WHERE a.capability=?`, capability).Scan(&b, &digest)
	} else {
		e = s.db.QueryRow(`SELECT package,digest FROM skill_versions WHERE capability=? AND version=?`, capability, version).Scan(&b, &digest)
	}
	if e != nil {
		return Package{}, e
	}
	if Digest(b) != digest {
		return Package{}, fmt.Errorf("installed package corrupted")
	}
	p, e := decode(b)
	if e == nil && (p.Capability != capability || (version != 0 && p.Version != version)) {
		return p, fmt.Errorf("installed identity mismatch")
	}
	return p, e
}
func (s *Store) Execute(capability, text string) (string, error) {
	p, e := s.load(capability, 0)
	if e != nil {
		return "", e
	}
	return run(p, text)
}
func (s *Store) Rollback(capability string, version int) error {
	if version < 1 {
		return fmt.Errorf("explicit version required")
	}
	if _, e := s.load(capability, version); e != nil {
		return e
	}
	_, e := s.db.Exec(`UPDATE skill_active SET version=? WHERE capability=?`, version, capability)
	return e
}
func (s *Store) Capabilities() ([]string, error) {
	rows, e := s.db.Query(`SELECT capability FROM skill_active ORDER BY capability`)
	if e != nil {
		return nil, e
	}
	var ids []string
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return nil, e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	for _, id := range ids {
		if _, e = s.load(id, 0); e != nil {
			return nil, e
		}
	}
	return ids, nil
}

// Inspect validates and evaluates a bounded package without installing it.
func Inspect(r io.Reader) (Package, string, error) {
	b, e := io.ReadAll(io.LimitReader(r, MaxPackageBytes+1))
	if e != nil {
		return Package{}, "", e
	}
	p, e := decode(b)
	if e != nil {
		return p, "", e
	}
	return p, Digest(b), nil
}

// ExecutePackage validates a bounded recipe and runs it without installation.
// Callers must enforce local approval before using this pure adapter.
func ExecutePackage(r io.Reader, text string) (string, error) {
	b, e := io.ReadAll(io.LimitReader(r, MaxPackageBytes+1))
	if e != nil {
		return "", e
	}
	p, e := decode(b)
	if e != nil {
		return "", e
	}
	return run(p, text)
}
