package agent

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/jiejhong/GooseCab/internal/protocol"
	"github.com/jiejhong/GooseCab/internal/skills"
	_ "modernc.org/sqlite"
)

// TextCapability is deliberately not a shell, filesystem, network or LLM tool.
const TextCapability = "cap.text.uppercase"
const MaxTextBytes = 4096

// Executor uses a durable, fail-closed journal. Keep the same journal when
// restarting an agent. A claimed call without a saved result is uncertain and
// must never be automatically executed again, even though this demo is pure.
type Executor struct {
	db     *sql.DB
	skills *skills.Store
	mu     sync.Mutex
}

func OpenExecutor(path string) (*Executor, error) {
	if strings.TrimSpace(path) == "" || path == ":memory:" || strings.HasPrefix(path, "file:") {
		return nil, fmt.Errorf("executor requires a persistent journal file path")
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
	for _, q := range []string{`PRAGMA busy_timeout=5000`, `PRAGMA journal_mode=WAL`, `PRAGMA synchronous=FULL`, `CREATE TABLE IF NOT EXISTS executions (room TEXT NOT NULL, agent TEXT NOT NULL, request TEXT NOT NULL, fingerprint TEXT NOT NULL, result TEXT, PRIMARY KEY(room,agent,request))`} {
		if _, err = db.Exec(q); err != nil {
			db.Close()
			return nil, err
		}
	}
	return &Executor{db: db}, nil
}
func (e *Executor) Close() error { return e.db.Close() }

// Handle executes only explicitly addressed, supported requests. The caller
// must use a single reader; send may fail at any point without causing a rerun.
func (e *Executor) Handle(ctx context.Context, room, agentID string, event map[string]any, send func(any) error) (bool, error) {
	if event["type"] != "capability.invoke" || event["room_id"] != room {
		return false, nil
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return false, err
	}
	var envelope protocol.Event
	if err = json.Unmarshal(raw, &envelope); err != nil {
		return false, err
	}
	body, err := json.Marshal(envelope.Payload)
	if err != nil {
		return false, err
	}
	var invoke protocol.CapabilityInvoke
	if err = json.Unmarshal(body, &invoke); err != nil {
		return false, err
	}
	if invoke.TargetAgentID != agentID || !e.supports(invoke.CapabilityID) {
		return false, nil
	}
	if strings.TrimSpace(invoke.RequestID) == "" || len(invoke.RequestID) > 128 || envelope.CorrelationID != invoke.RequestID || envelope.Actor.Kind != "agent" || envelope.Actor.ID == "" {
		return true, &InvocationError{invoke.RequestID, "invalid execution envelope"}
	}
	// Canonical JSON includes the caller identity to prevent request-ID reuse.
	canonical, _ := json.Marshal(struct {
		Caller string
		Invoke protocol.CapabilityInvoke
	}{envelope.Actor.ID, invoke})
	sum := sha256.Sum256(canonical)
	fingerprint := hex.EncodeToString(sum[:])
	e.mu.Lock()
	defer e.mu.Unlock()
	claim, err := e.db.ExecContext(ctx, `INSERT INTO executions(room,agent,request,fingerprint) VALUES(?,?,?,?) ON CONFLICT DO NOTHING`, room, agentID, invoke.RequestID, fingerprint)
	if err != nil {
		return true, fmt.Errorf("claim invocation: %w", err)
	}
	count, err := claim.RowsAffected()
	if err != nil {
		return true, err
	}
	if count == 0 {
		var previous string
		var saved sql.NullString
		if err = e.db.QueryRowContext(ctx, `SELECT fingerprint,result FROM executions WHERE room=? AND agent=? AND request=?`, room, agentID, invoke.RequestID).Scan(&previous, &saved); err != nil {
			return true, err
		}
		if previous != fingerprint {
			return true, &InvocationError{invoke.RequestID, "request ID conflict: original invocation was not changed"}
		}
		result := protocol.CapabilityResult{RequestID: invoke.RequestID, Status: "unknown", Error: "Execution was claimed but no durable result exists. Check the worker; do not automatically retry."}
		if saved.Valid {
			result = protocol.CapabilityResult{}
			if err = json.Unmarshal([]byte(saved.String), &result); err != nil {
				return true, &InvocationError{invoke.RequestID, "saved result is unreadable; inspect this invocation without retrying"}
			}
		}
		if result.Status == "completed" {
			if err = sendProgress(send, invoke.RequestID, "completed", 100, "Execution complete; replaying saved result"); err != nil {
				return true, err
			}
		}
		return true, sendResult(send, result)
	}
	// If even the first progress write fails, leave the claim uncertain. Never
	// infer that a network error proves the gateway did not receive a message.
	if err = sendProgress(send, invoke.RequestID, "running", 0, "Executing bounded text capability"); err != nil {
		return true, err
	}
	result := e.execute(invoke)
	saved, err := json.Marshal(result)
	if err != nil {
		return true, err
	}
	if _, err = e.db.ExecContext(ctx, `UPDATE executions SET result=? WHERE room=? AND agent=? AND request=?`, string(saved), room, agentID, invoke.RequestID); err != nil {
		return true, fmt.Errorf("persist execution result: %w", err)
	}
	if result.Status == "completed" {
		if err = sendProgress(send, invoke.RequestID, "completed", 100, "Execution complete; publishing result"); err != nil {
			return true, err
		}
	}
	return true, sendResult(send, result)
}

func executeText(invoke protocol.CapabilityInvoke) protocol.CapabilityResult {
	r := protocol.CapabilityResult{RequestID: invoke.RequestID, Status: "failed"}
	raw, err := json.Marshal(invoke.Payload)
	if err != nil {
		r.Error = "Invalid text payload"
		return r
	}
	var input struct {
		Text *string `json:"text"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&input); err != nil || input.Text == nil || !utf8.ValidString(*input.Text) || len(*input.Text) > MaxTextBytes {
		r.Error = "Expected only a UTF-8 text string, maximum 4096 bytes"
		return r
	}
	output := strings.ToUpper(*input.Text)
	if len(output) > MaxTextBytes {
		r.Error = "Uppercase result exceeds 4096 bytes"
		return r
	}
	r.Status = "completed"
	r.ResultAvailable = true
	r.Output = &protocol.TextOutput{Text: output, MediaType: "text/plain"}
	return r
}
func sendProgress(send func(any) error, id, status string, percent int, message string) error {
	return send(struct {
		Type string `json:"type"`
		protocol.CapabilityProgress
	}{"capability.progress", protocol.CapabilityProgress{RequestID: id, Status: status, Percent: percent, Message: message}})
}
func sendResult(send func(any) error, result protocol.CapabilityResult) error {
	return send(struct {
		Type string `json:"type"`
		protocol.CapabilityResult
	}{"capability.result", result})
}

// InvocationError isolates malformed or conflicting calls from healthy work.
// Storage/transport errors remain fatal because safe execution depends on them.
type InvocationError struct{ RequestID, Message string }

func (e *InvocationError) Error() string { return e.Message }

// RunExecutor keeps one outbound connection. On disconnect it stops instead
// of spinning on a poisoned websocket read deadline. Restart with the same
// journal to replay cached results; it never automatically resubmits a task.
func RunExecutor(ctx context.Context, c *Client, e *Executor, onEvent func(map[string]any) error) error {
	// A restart need not re-publish results that are already durably available.
	// Fold the entire pre-welcome snapshot first, rather than reacting to its
	// earliest invocation while overlooking a later result for that same call.
	var replay []map[string]any
	for {
		msg, ok := c.dequeue()
		if !ok {
			break
		}
		replay = append(replay, msg)
	}
	terminal := terminalReplayIDs(replay, c.RoomID, c.AgentID)
	blocked := map[string]bool{}
	handle := func(msg map[string]any, replayed bool) error {
		if onEvent != nil {
			if err := onEvent(msg); err != nil {
				return err
			}
		}
		if isProtocolError(msg) {
			id, _ := msg["request_id"].(string)
			if msg["code"] == "invalid_execution" && id != "" {
				blocked[id] = true
				return nil
			}
			return fmt.Errorf("gateway rejected a worker message: %v", msg["code"])
		}
		id, _ := msg["correlation_id"].(string)
		if blocked[id] || (replayed && terminal[id]) {
			return nil
		}
		_, err := e.Handle(ctx, c.RoomID, c.AgentID, msg, c.Write)
		var invalid *InvocationError
		if errors.As(err, &invalid) {
			blocked[invalid.RequestID] = true
			if onEvent != nil {
				return onEvent(map[string]any{"type": "client.execution_error", "request_id": invalid.RequestID, "message": invalid.Message})
			}
			return nil
		}
		return err
	}
	for _, msg := range replay {
		if err := handle(msg, true); err != nil {
			return err
		}
	}
	for {
		msg, err := c.ReadContext(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
		if err = handle(msg, false); err != nil {
			return err
		}
	}
}

func terminalReplayIDs(replay []map[string]any, room, agentID string) map[string]bool {
	known := map[string]bool{}
	terminal := map[string]bool{}
	for _, msg := range replay {
		if msg["room_id"] != room {
			continue
		}
		id, _ := msg["correlation_id"].(string)
		if id == "" {
			continue
		}
		payload, _ := msg["payload"].(map[string]any)
		if msg["type"] == "capability.invoke" && isExecutableCapability(payload["capability_id"]) && payload["target_agent_id"] == agentID && payload["request_id"] == id {
			known[id] = true
		}
		actor, _ := msg["actor"].(map[string]any)
		if known[id] && msg["type"] == "capability.result" && actor["kind"] == "agent" && actor["id"] == agentID && payload["request_id"] == id {
			if payload["status"] == "completed" || payload["status"] == "failed" {
				terminal[id] = true
			}
		}
	}
	return terminal
}

// UseSkills attaches a local store before RunExecutor starts. The caller owns it.
func (e *Executor) UseSkills(s *skills.Store) { e.skills = s }
func (e *Executor) Capabilities() ([]string, error) {
	ids := []string{TextCapability}
	if e.skills == nil {
		return ids, nil
	}
	learned, err := e.skills.Capabilities()
	return append(ids, learned...), err
}
func (e *Executor) supports(id string) bool {
	if id == TextCapability {
		return true
	}
	if e.skills == nil || !skills.ValidCapability(id) {
		return false
	}
	ids, err := e.skills.Capabilities()
	if err != nil {
		return false
	}
	for _, known := range ids {
		if id == known {
			return true
		}
	}
	return false
}
func isExecutableCapability(v any) bool {
	id, _ := v.(string)
	return id == TextCapability || skills.ValidCapability(id)
}
func (e *Executor) execute(invoke protocol.CapabilityInvoke) protocol.CapabilityResult {
	if invoke.CapabilityID == TextCapability {
		return executeText(invoke)
	}
	r := protocol.CapabilityResult{RequestID: invoke.RequestID, Status: "failed"}
	b, err := json.Marshal(invoke.Payload)
	if err != nil {
		r.Error = "Invalid text payload"
		return r
	}
	var input struct {
		Text *string `json:"text"`
	}
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if err = d.Decode(&input); err != nil || input.Text == nil {
		r.Error = "Expected only a text string"
		return r
	}
	output, err := e.skills.Execute(invoke.CapabilityID, *input.Text)
	if err != nil {
		r.Error = err.Error()
		return r
	}
	r.Status = "completed"
	r.ResultAvailable = true
	r.Output = &protocol.TextOutput{Text: output, MediaType: "text/plain"}
	return r
}
