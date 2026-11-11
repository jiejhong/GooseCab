// Package cascade links explicit text-capability routes across two gateways.
// It is an agent, not an event replicator or a file store.
package cascade

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jiejhong/GooseCab/internal/agent"
	"github.com/jiejhong/GooseCab/internal/protocol"
	_ "modernc.org/sqlite"
)

type Endpoint struct {
	URL   string `json:"url"`
	Room  string `json:"room"`
	Token string `json:"-"`
}
type Route struct {
	Worker     string `json:"worker"`
	Capability string `json:"capability"`
}
type Config struct {
	ID         string        `json:"id"`
	Mode       string        `json:"mode"` // leader or flat
	Upstream   Endpoint      `json:"upstream"`
	Downstream Endpoint      `json:"downstream"`
	Routes     []Route       `json:"routes"`
	MaxHops    int           `json:"max_hops"`
	Timeout    time.Duration `json:"-"`
}

func (c Config) Identity(worker string) string {
	if c.Mode == "flat" {
		return c.ID + "/" + worker
	}
	return c.ID
}
func (c Config) validate() error {
	if c.ID == "" || len(c.ID) > 100 || strings.Contains(c.ID, "/") {
		return fmt.Errorf("bridge ID must be nonempty, at most 100 bytes, without slash")
	}
	if c.Mode != "leader" && c.Mode != "flat" {
		return fmt.Errorf("mode must be leader or flat")
	}
	if c.Upstream.URL == "" || c.Downstream.URL == "" || c.Upstream.Room == "" || c.Downstream.Room == "" {
		return fmt.Errorf("two explicit gateway URLs and rooms are required")
	}
	if c.Upstream.URL == c.Downstream.URL {
		return fmt.Errorf("gateways must differ")
	}
	if c.MaxHops < 1 || c.MaxHops > 8 || c.Timeout <= 0 || len(c.Routes) == 0 || len(c.Routes) > 64 {
		return fmt.Errorf("require 1..8 hops, positive timeout and 1..64 routes")
	}
	seen := map[string]bool{}
	for _, r := range c.Routes {
		// Safe text workers only; no arbitrary shell or file-capability forwarding.
		if r.Worker == "" || len(c.Identity(r.Worker)) > 256 || r.Worker == c.ID || !protocol.IsExecutableTextCapability(r.Capability) {
			return fmt.Errorf("invalid explicit text route")
		}
		k := c.Identity(r.Worker) + "\x00" + r.Capability
		if seen[k] {
			return fmt.Errorf("ambiguous route for %s", k)
		}
		seen[k] = true
	}
	return nil
}

type Bridge struct {
	config Config
	db     *sql.DB
}

func Open(c Config, path string) (*Bridge, error) {
	c.Routes = append([]Route(nil), c.Routes...)
	if err := c.validate(); err != nil {
		return nil, err
	}
	if path == "" || path == ":memory:" || strings.HasPrefix(path, "file:") {
		return nil, fmt.Errorf("persistent bridge journal required")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, q := range []string{`PRAGMA busy_timeout=5000`, `PRAGMA journal_mode=WAL`, `PRAGMA synchronous=FULL`, `CREATE TABLE IF NOT EXISTS config (singleton INTEGER PRIMARY KEY CHECK(singleton=1), fingerprint TEXT NOT NULL)`, `CREATE TABLE IF NOT EXISTS mappings (proxy TEXT NOT NULL, upstream TEXT NOT NULL, downstream TEXT NOT NULL UNIQUE, worker TEXT NOT NULL, fingerprint TEXT NOT NULL, terminal INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(proxy,upstream))`} {
		if _, err = db.Exec(q); err != nil {
			db.Close()
			return nil, err
		}
	}
	raw, _ := json.Marshal(c)
	fingerprint := digest(raw)
	_, err = db.Exec(`INSERT INTO config VALUES(1,?) ON CONFLICT DO NOTHING`, fingerprint)
	var old string
	if err == nil {
		err = db.QueryRow(`SELECT fingerprint FROM config WHERE singleton=1`).Scan(&old)
	}
	if err == nil && old != fingerprint {
		err = fmt.Errorf("journal belongs to different route configuration; do not reroute existing requests")
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Bridge{c, db}, nil
}
func (b *Bridge) Close() error { return b.db.Close() }
func digest(v []byte) string   { s := sha256.Sum256(v); return hex.EncodeToString(s[:]) }

type frame struct {
	proxy string
	msg   map[string]any
	err   error
}
type pending struct {
	proxy, id string
	deadline  time.Time
}

func decode(v any, to any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, to)
}
func result(c *agent.Client, id, status, message string) error {
	return c.Write(struct {
		Type string `json:"type"`
		protocol.CapabilityResult
	}{"capability.result", protocol.CapabilityResult{RequestID: id, Status: status, Error: message}})
}

// Run stops on connection loss. Restart with exactly the same configuration
// and journal. Existing durable claims are never automatically resubmitted.
func (b *Bridge) Run(ctx context.Context) (runErr error) {
	defer func() {
		if ctx.Err() != nil {
			runErr = nil
		}
	}()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c := b.config
	up := map[string]*agent.Client{}
	defer func() {
		for _, a := range up {
			_ = a.Close()
		}
	}()
	caps := map[string][]string{}
	for _, r := range c.Routes {
		id := c.Identity(r.Worker)
		caps[id] = append(caps[id], r.Capability)
	}
	for id, cc := range caps {
		a, err := agent.DialWithTokenContext(ctx, c.Upstream.URL, c.Upstream.Room, id, cc, c.Upstream.Token)
		if err != nil {
			return err
		}
		up[id] = a
	}
	down, err := agent.DialWithTokenContext(ctx, c.Downstream.URL, c.Downstream.Room, c.ID+"/bridge", nil, c.Downstream.Token)
	if err != nil {
		return err
	}
	defer down.Close()
	frames := make(chan frame, 128)
	read := func(id string, a *agent.Client) {
		for {
			m, e := a.ReadContext(ctx)
			select {
			case frames <- frame{id, m, e}:
			case <-ctx.Done():
				return
			}
			if e != nil {
				return
			}
		}
	}
	for id, a := range up {
		go read(id, a)
	}
	go read("", down)
	waiting := map[string]pending{}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			for id, p := range waiting {
				if time.Now().After(p.deadline) {
					if _, err = b.db.ExecContext(ctx, `UPDATE mappings SET terminal=2 WHERE downstream=? AND terminal=0`, id); err != nil {
						return err
					}
					if err = result(up[p.proxy], p.id, "unknown", "Downstream outcome unknown; it may still execute. Retain request ID and journal; do not resubmit."); err != nil {
						return err
					}
					delete(waiting, id)
				}
			}
		case f := <-frames:
			if f.err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("bridge transport lost; retain journal: %w", f.err)
			}
			if f.msg["type"] == "error" && f.msg["event_id"] == nil {
				if f.msg["code"] == "invalid_execution" && f.msg["request_id"] != nil {
					continue
				}
				return fmt.Errorf("gateway rejected bridge message: %v", f.msg["code"])
			}
			if f.proxy != "" {
				// Fold authoritative upstream terminal history, including replay after an
				// uncertain write. Unknown forbids a later running regression but permits
				// completed progress and an authoritative result to reconcile it.
				if f.msg["type"] == "capability.result" && f.msg["room_id"] == c.Upstream.Room {
					var ev protocol.Event
					var rr protocol.CapabilityResult
					if decode(f.msg, &ev) == nil && decode(ev.Payload, &rr) == nil && ev.Actor.Kind == "agent" && ev.Actor.ID == f.proxy && rr.RequestID == ev.CorrelationID {
						state := 0
						if rr.Status == "unknown" {
							state = 2
						}
						if rr.Status == "completed" || rr.Status == "failed" {
							state = 1
						}
						if state != 0 {
							if _, err = b.db.ExecContext(ctx, `UPDATE mappings SET terminal=? WHERE proxy=? AND upstream=? AND terminal!=1`, state, f.proxy, rr.RequestID); err != nil {
								return err
							}
						}
					}
					continue
				}
				if f.msg["type"] != "capability.invoke" || f.msg["room_id"] != c.Upstream.Room {
					continue
				}
				var e protocol.Event
				var v protocol.CapabilityInvoke
				if decode(f.msg, &e) != nil || decode(e.Payload, &v) != nil || v.TargetAgentID != f.proxy {
					continue
				}
				if e.Actor.Kind != "agent" || e.Actor.ID == "" || v.RequestID == "" || len(v.RequestID) > 128 || e.CorrelationID != v.RequestID {
					continue
				}
				var worker string
				for _, r := range c.Routes {
					if c.Identity(r.Worker) == f.proxy && r.Capability == v.CapabilityID {
						worker = r.Worker
					}
				}
				reason := ""
				if worker == "" {
					reason = "Capability is not in this bridge's explicit route allowlist"
				}
				if len(v.Route) >= c.MaxHops {
					reason = "Cascade hop limit reached"
				}
				for _, hop := range v.Route {
					if hop == c.ID {
						reason = "Cascade loop rejected"
					}
				}
				raw, _ := json.Marshal(v.Payload)
				var input map[string]any
				_ = json.Unmarshal(raw, &input)
				text, ok := input["text"].(string)
				if !ok || len(input) != 1 || len(text) > agent.MaxTextBytes || !utf8.ValidString(text) {
					reason = "Bridge accepts only bounded text payloads, never files"
				}
				if reason != "" {
					if err = result(up[f.proxy], v.RequestID, "failed", reason); err != nil {
						return err
					}
					continue
				}
				canonical, _ := json.Marshal(struct {
					Caller string
					Invoke protocol.CapabilityInvoke
				}{e.Actor.ID, v})
				fingerprint := digest(canonical)
				key, _ := json.Marshal([]string{c.ID, c.Upstream.Room, f.proxy, v.RequestID})
				downstreamID := "cascade-" + digest(key)
				claim, err := b.db.ExecContext(ctx, `INSERT INTO mappings(proxy,upstream,downstream,worker,fingerprint) VALUES(?,?,?,?,?) ON CONFLICT DO NOTHING`, f.proxy, v.RequestID, downstreamID, worker, fingerprint)
				if err != nil {
					return err
				}
				n, err := claim.RowsAffected()
				if err != nil {
					return err
				}
				if n == 0 {
					var old string
					var terminal int
					if err = b.db.QueryRowContext(ctx, `SELECT fingerprint,terminal FROM mappings WHERE proxy=? AND upstream=?`, f.proxy, v.RequestID).Scan(&old, &terminal); err != nil {
						return err
					}
					if old != fingerprint {
						return fmt.Errorf("upstream request ID conflict")
					}
					if terminal != 1 {
						waiting[downstreamID] = pending{f.proxy, v.RequestID, time.Now().Add(c.Timeout)}
					}
					continue
				}
				route := append(append([]string{}, v.Route...), c.ID)
				if err = up[f.proxy].SendEvent("cascade.forwarded", map[string]any{"upstream_request_id": v.RequestID, "downstream_request_id": downstreamID, "upstream_room": c.Upstream.Room, "downstream_room": c.Downstream.Room, "proxy": f.proxy, "worker": worker, "route": route}); err != nil {
					return err
				}
				v.RequestID = downstreamID
				v.TargetAgentID = worker
				v.Route = route
				if err = down.Write(struct {
					Type string `json:"type"`
					protocol.CapabilityInvoke
				}{"capability.invoke", v}); err != nil {
					return err
				}
				waiting[downstreamID] = pending{f.proxy, e.CorrelationID, time.Now().Add(c.Timeout)}
			} else {
				typ, _ := f.msg["type"].(string)
				if (typ != "capability.progress" && typ != "capability.result") || f.msg["room_id"] != c.Downstream.Room {
					continue
				}
				var e protocol.Event
				if decode(f.msg, &e) != nil {
					continue
				}
				var proxy, id, worker string
				var terminal int
				err = b.db.QueryRowContext(ctx, `SELECT proxy,upstream,worker,terminal FROM mappings WHERE downstream=?`, e.CorrelationID).Scan(&proxy, &id, &worker, &terminal)
				if err == sql.ErrNoRows {
					continue
				}
				if err != nil {
					return err
				}
				if e.Actor.Kind != "agent" || e.Actor.ID != worker || up[proxy] == nil {
					continue
				}
				if typ == "capability.progress" {
					var p protocol.CapabilityProgress
					if decode(e.Payload, &p) != nil || p.RequestID != e.CorrelationID {
						continue
					}
					if terminal != 0 && p.Status != "completed" {
						continue
					}
					p.RequestID = id
					err = up[proxy].Write(struct {
						Type string `json:"type"`
						protocol.CapabilityProgress
					}{typ, p})
				} else {
					var r protocol.CapabilityResult
					if decode(e.Payload, &r) != nil || r.RequestID != e.CorrelationID {
						continue
					}
					r.RequestID = id
					if terminal != 0 && r.Status != "completed" && r.Status != "failed" {
						continue
					}
					// A replay can arrive independently of its completed progress window.
					if r.Status == "completed" {
						err = up[proxy].Write(struct {
							Type string `json:"type"`
							protocol.CapabilityProgress
						}{"capability.progress", protocol.CapabilityProgress{RequestID: id, Status: "completed", Percent: 100}})
						if err != nil {
							return err
						}
					}
					err = up[proxy].Write(struct {
						Type string `json:"type"`
						protocol.CapabilityResult
					}{typ, r})
					if err == nil && r.Status == "unknown" {
						_, err = b.db.ExecContext(ctx, `UPDATE mappings SET terminal=2 WHERE downstream=? AND terminal=0`, e.CorrelationID)
					}
					if err == nil && (r.Status == "completed" || r.Status == "failed") {
						_, err = b.db.ExecContext(ctx, `UPDATE mappings SET terminal=1 WHERE downstream=?`, e.CorrelationID)
						delete(waiting, e.CorrelationID)
					}
				}
				if err != nil {
					return err
				}
			}
		}
	}
}
