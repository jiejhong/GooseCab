package orchestrator

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jiejhong/GooseCab/internal/protocol"
	_ "modernc.org/sqlite"
)

type State struct {
	ID        string                     `json:"id"`
	Target    string                     `json:"target"`
	RequestID string                     `json:"request_id"`
	Status    string                     `json:"status"`
	Input     string                     `json:"input,omitempty"`
	Result    *protocol.CapabilityResult `json:"result,omitempty"`
	Error     string                     `json:"error,omitempty"`
}
type Event struct {
	Seq  int64  `json:"seq"`
	Time string `json:"time"`
	Task State  `json:"task"`
}
type Journal struct {
	db     *sql.DB
	lock   string
	Plan   Plan
	states map[string]State
}

// Open owns an exclusive journal lock until Close. Never delete a stale lock
// until the previous process has been independently confirmed stopped.
func Open(path string, p Plan) (*Journal, error) {
	if e := p.Validate(); e != nil {
		return nil, e
	}
	if strings.TrimSpace(path) == "" || path == ":memory:" || strings.HasPrefix(path, "file:") {
		return nil, fmt.Errorf("persistent local journal path required")
	}
	lock := path + ".lock"
	f, e := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return nil, fmt.Errorf("journal locked or inaccessible (verify old process stopped before removing %s): %w", lock, e)
	}
	_, e = f.WriteString(fmt.Sprintf("pid=%d\n", os.Getpid()))
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		os.Remove(lock)
		return nil, e
	}
	j := &Journal{lock: lock, Plan: p, states: map[string]State{}}
	fail := func(e error) (*Journal, error) { j.Close(); return nil, e }
	f, e = os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return fail(e)
	}
	if e = f.Close(); e != nil {
		return fail(e)
	}
	j.db, e = sql.Open("sqlite", path)
	if e != nil {
		return fail(e)
	}
	j.db.SetMaxOpenConns(1)
	for _, q := range []string{`PRAGMA busy_timeout=5000`, `PRAGMA journal_mode=WAL`, `PRAGMA synchronous=FULL`, `CREATE TABLE IF NOT EXISTS plan (id INTEGER PRIMARY KEY CHECK(id=1), body TEXT NOT NULL)`, `CREATE TABLE IF NOT EXISTS tasks (id TEXT PRIMARY KEY, body TEXT NOT NULL)`, `CREATE TABLE IF NOT EXISTS task_events (seq INTEGER PRIMARY KEY AUTOINCREMENT, time TEXT NOT NULL, body TEXT NOT NULL)`} {
		if _, e = j.db.Exec(q); e != nil {
			return fail(e)
		}
	}
	b, _ := json.Marshal(p)
	var old string
	e = j.db.QueryRow(`SELECT body FROM plan WHERE id=1`).Scan(&old)
	if e == sql.ErrNoRows {
		tx, e := j.db.Begin()
		if e != nil {
			return fail(e)
		}
		_, e = tx.Exec(`INSERT INTO plan(id,body) VALUES(1,?)`, string(b))
		for _, t := range p.Tasks {
			if e != nil {
				break
			}
			s := State{ID: t.ID, Target: t.Target, RequestID: p.RequestID(t.ID), Status: "pending"}
			v, _ := json.Marshal(s)
			_, e = tx.Exec(`INSERT INTO tasks(id,body) VALUES(?,?)`, t.ID, string(v))
			if e == nil {
				_, e = tx.Exec(`INSERT INTO task_events(time,body) VALUES(?,?)`, time.Now().UTC().Format(time.RFC3339Nano), string(v))
			}
		}
		if e != nil {
			tx.Rollback()
			return fail(e)
		}
		if e = tx.Commit(); e != nil {
			return fail(e)
		}
	} else if e != nil {
		return fail(e)
	} else if old != string(b) {
		return fail(fmt.Errorf("journal plan conflict: use the original exact plan; never change uncertain assignments"))
	}
	rows, e := j.db.Query(`SELECT body FROM tasks`)
	if e != nil {
		return fail(e)
	}
	for rows.Next() {
		var b string
		var s State
		if e = rows.Scan(&b); e == nil {
			e = json.Unmarshal([]byte(b), &s)
		}
		if e != nil {
			rows.Close()
			return fail(e)
		}
		j.states[s.ID] = s
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return fail(e)
	}
	if len(j.states) != len(p.Tasks) {
		return fail(fmt.Errorf("incomplete journal; manual inspection required"))
	}
	for _, t := range p.Tasks {
		s, ok := j.states[t.ID]
		if !ok || s.RequestID != p.RequestID(t.ID) || s.Target != t.Target {
			return fail(fmt.Errorf("journal assignment mismatch"))
		}
		switch s.Status {
		case "pending", "completed", "failed", "unknown", "blocked":
		case "running":
			s.Status = "unknown"
			s.Error = "Interrupted after durable assignment; never automatically resubmit"
			if e = j.save(s); e != nil {
				return fail(e)
			}
		default:
			return fail(fmt.Errorf("invalid journal status"))
		}
	}
	return j, nil
}
func (j *Journal) Close() error {
	var e error
	if j.db != nil {
		e = j.db.Close()
	}
	if j.lock != "" {
		if x := os.Remove(j.lock); e == nil {
			e = x
		}
		j.lock = ""
	}
	return e
}
func (j *Journal) save(s State) error {
	b, e := json.Marshal(s)
	if e != nil {
		return e
	}
	tx, e := j.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.Exec(`UPDATE tasks SET body=? WHERE id=?`, string(b), s.ID); e != nil {
		return e
	}
	if _, e = tx.Exec(`INSERT INTO task_events(time,body) VALUES(?,?)`, time.Now().UTC().Format(time.RFC3339Nano), string(b)); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	j.states[s.ID] = s
	return nil
}
func (j *Journal) States() []State {
	out := make([]State, 0, len(j.Plan.Tasks))
	for _, t := range j.Plan.Tasks {
		out = append(out, j.states[t.ID])
	}
	return out
}
func (j *Journal) Events() ([]Event, error) {
	rows, e := j.db.Query(`SELECT seq,time,body FROM task_events ORDER BY seq`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var v Event
		var b string
		if e = rows.Scan(&v.Seq, &v.Time, &b); e != nil {
			return nil, e
		}
		if e = json.Unmarshal([]byte(b), &v.Task); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
