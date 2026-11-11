// Package mcpclient provides explicitly approved, HTTP-only MCP tools. Imported
// manifests are inert; all authority and credential bindings are local.
package mcpclient

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	_ "modernc.org/sqlite"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
)

const MaxManifestBytes = 65536

var safeName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,47}$`)
var toolName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type Tool struct {
	Capability  string          `json:"capability"`
	Name        string          `json:"name"`
	InputSchema json.RawMessage `json:"input_schema"`
}
type Manifest struct {
	Format   int    `json:"format"`
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"`
	Tools    []Tool `json:"tools"`
}

// Binding is exclusively local approval state, never an import/export field.
type Binding struct {
	AllowedCallers    []string
	AllowedRooms      []string
	TokenEnv          string
	AllowPrivate      bool
	AllowHTTPLoopback bool
}
type Entry struct {
	Digest   string   `json:"digest"`
	Manifest Manifest `json:"manifest"`
	Approved bool     `json:"approved"`
}
type Store struct{ db *sql.DB }

func Digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func endpoint(s string) (*url.URL, error) {
	u, e := url.Parse(s)
	if e != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || (u.Scheme != "https" && u.Scheme != "http") || len(s) > 2048 {
		return nil, errors.New("invalid credential-free HTTP endpoint")
	}
	return u, nil
}
func parse(b []byte) (Manifest, error) {
	var m Manifest
	if len(b) > MaxManifestBytes {
		return m, errors.New("manifest too large")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&m) != nil || d.Decode(new(any)) != io.EOF {
		return m, errors.New("invalid manifest; only declarative HTTP tool fields are allowed")
	}
	if m.Format != 1 || !safeName.MatchString(m.Name) || len(m.Tools) == 0 || len(m.Tools) > 32 {
		return m, errors.New("invalid manifest identity or tools")
	}
	if _, e := endpoint(m.Endpoint); e != nil {
		return m, e
	}
	seen := map[string]bool{}
	names := map[string]bool{}
	for _, t := range m.Tools {
		var schema map[string]any
		if !strings.HasPrefix(t.Capability, "cap.mcp.") || !safeName.MatchString(strings.TrimPrefix(t.Capability, "cap.mcp.")) || !toolName.MatchString(t.Name) || seen[t.Capability] || names[t.Name] || json.Unmarshal(t.InputSchema, &schema) != nil || schema == nil || schema["type"] != "object" {
			return m, errors.New("invalid tool identity or object input schema")
		}
		seen[t.Capability] = true
		names[t.Name] = true
	}
	return m, nil
}
func Open(path string) (*Store, error) {
	if path == "" || path == ":memory:" || strings.HasPrefix(path, "file:") {
		return nil, errors.New("persistent local MCP database required")
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	f.Close()
	db, e := sql.Open("sqlite", path)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	_, e = db.Exec(`PRAGMA busy_timeout=5000; CREATE TABLE IF NOT EXISTS mcp_manifests(digest TEXT PRIMARY KEY, body BLOB NOT NULL); CREATE TABLE IF NOT EXISTS mcp_approvals(digest TEXT PRIMARY KEY, token_env TEXT NOT NULL, private INTEGER NOT NULL, loopback_http INTEGER NOT NULL, callers TEXT NOT NULL, rooms TEXT NOT NULL);`)
	if e != nil {
		db.Close()
		return nil, e
	}
	return &Store{db}, nil
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Import(b []byte) (string, error) {
	if _, e := parse(b); e != nil {
		return "", e
	}
	d := Digest(b)
	_, e := s.db.Exec(`INSERT OR IGNORE INTO mcp_manifests VALUES(?,?)`, d, b)
	return d, e
}
func (s *Store) Export(d string) ([]byte, error) {
	var b []byte
	e := s.db.QueryRow(`SELECT body FROM mcp_manifests WHERE digest=?`, d).Scan(&b)
	return b, e
}
func (s *Store) Approve(d string, b Binding) error {
	if len(b.AllowedCallers) == 0 {
		return errors.New("explicit allowed caller IDs required")
	}
	for _, ids := range [][]string{b.AllowedCallers, b.AllowedRooms} {
		for _, id := range ids {
			if id == "" || id == "*" || len(id) > 128 || strings.ContainsAny(id, "\r\n") {
				return errors.New("invalid exact caller or room ID")
			}
		}
	}
	raw, e := s.Export(d)
	if e != nil {
		return errors.New("manifest digest not imported")
	}
	if Digest(raw) != d {
		return errors.New("manifest digest mismatch")
	}
	m, e := parse(raw)
	if e != nil {
		return e
	}
	u, _ := endpoint(m.Endpoint)
	if b.TokenEnv != "" && !envName.MatchString(b.TokenEnv) {
		return errors.New("invalid local token environment binding")
	}
	if u.Scheme == "http" {
		ip := net.ParseIP(u.Hostname())
		if !b.AllowHTTPLoopback || ip == nil || !ip.IsLoopback() {
			return errors.New("HTTP requires explicit literal-loopback test approval")
		}
	}
	callers, _ := json.Marshal(b.AllowedCallers)
	rooms, _ := json.Marshal(b.AllowedRooms)
	_, e = s.db.Exec(`INSERT OR REPLACE INTO mcp_approvals VALUES(?,?,?,?,?,?)`, d, b.TokenEnv, b.AllowPrivate, b.AllowHTTPLoopback, string(callers), string(rooms))
	return e
}
func (s *Store) List() ([]Entry, error) {
	rows, e := s.db.Query(`SELECT m.digest,m.body,a.digest IS NOT NULL FROM mcp_manifests m LEFT JOIN mcp_approvals a ON m.digest=a.digest ORDER BY m.digest`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		var v Entry
		var b []byte
		if e = rows.Scan(&v.Digest, &b, &v.Approved); e != nil {
			return nil, e
		}
		v.Manifest, e = parse(b)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Revoke removes local authority only; the transferable manifest is retained.
func (s *Store) Revoke(d string) error {
	if len(d) != 64 {
		return errors.New("exact manifest SHA256 required")
	}
	_, e := hex.DecodeString(d)
	if e != nil {
		return errors.New("invalid manifest SHA256")
	}
	_, e = s.db.Exec(`DELETE FROM mcp_approvals WHERE digest=?`, d)
	return e
}
func (s *Store) binding(d string) (Binding, error) {
	var b Binding
	var callers, rooms string
	e := s.db.QueryRow(`SELECT token_env,private,loopback_http,callers,rooms FROM mcp_approvals WHERE digest=?`, d).Scan(&b.TokenEnv, &b.AllowPrivate, &b.AllowHTTPLoopback, &callers, &rooms)
	if e != nil {
		return b, e
	}
	if json.Unmarshal([]byte(callers), &b.AllowedCallers) != nil || json.Unmarshal([]byte(rooms), &b.AllowedRooms) != nil {
		return b, errors.New("invalid local approval")
	}
	return b, nil
}
