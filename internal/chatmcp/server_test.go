package chatmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type fakeBackend struct {
	calls int
	send  SendArgs
	read  ReadArgs
}

func (f *fakeBackend) Send(_ context.Context, a SendArgs) (any, error) {
	f.calls++
	f.send = a
	return map[string]any{"ok": true}, nil
}
func (f *fakeBackend) Read(_ context.Context, a ReadArgs) (any, error) {
	f.calls++
	f.read = a
	return []any{}, nil
}
func (f *fakeBackend) Discover(context.Context, DiscoverArgs) (any, error) {
	f.calls++
	return []any{}, nil
}
func runRPC(t *testing.T, f Backend, lines ...string) []map[string]any {
	t.Helper()
	var out bytes.Buffer
	if err := (&Server{Backend: f}).Serve(context.Background(), strings.NewReader(strings.Join(lines, "\n")+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(&out)
	dec.UseNumber()
	var results []map[string]any
	for dec.More() {
		var r map[string]any
		if err := dec.Decode(&r); err != nil {
			t.Fatal(err)
		}
		results = append(results, r)
	}
	return results
}

const initRPC = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`

func TestProtocolAndTools(t *testing.T) {
	f := &fakeBackend{}
	got := runRPC(t, f, initRPC, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, `{"jsonrpc":"2.0","id":"list","method":"tools/list"}`, `{"jsonrpc":"2.0","id":9007199254740993,"method":"tools/call","params":{"name":"chat_send","arguments":{"message_id":"a-uuid","text":"hello","reply_to_event_id":"prior"}}}`, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"room_read","arguments":{}}}`)
	if len(got) != 4 || len(got[1]["result"].(map[string]any)["tools"].([]any)) != 3 {
		t.Fatalf("%#v", got)
	}
	if got[2]["id"].(json.Number).String() != "9007199254740993" {
		t.Fatal("ID lost precision")
	}
	if f.send.ReplyTo != "prior" || f.read.Limit != 20 || f.calls != 2 {
		t.Fatal(f)
	}
}
func TestRejectedCallsAndNotifications(t *testing.T) {
	for _, args := range []string{
		`{"name":"chat_send","arguments":{"message_id":"x","text":"ok","room_id":"other"}}`,
		`{"name":"chat_send","arguments":{"message_id":"x","text":"ok","agent_id":"other"}}`,
		`{"name":"room_read","arguments":{"url":"http://evil"}}`,
		`{"name":"room_read","arguments":{"limit":51}}`,
		`{"name":"room_read","arguments":{"after_cursor":"0"}}`,
		`{"name":"room_read","arguments":null}`,
		`{"name":"capability_discover","arguments":{"token":"secret"}}`,
		`{"name":"capability_discover","arguments":{"kind":"shell"}}`,
		`{"name":"invoke","arguments":{}}`,
	} {
		f := &fakeBackend{}
		got := runRPC(t, f, initRPC, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":`+args+`}`)
		if !got[1]["result"].(map[string]any)["isError"].(bool) || f.calls != 0 {
			t.Fatal(args, got)
		}
	}
	f := &fakeBackend{}
	got := runRPC(t, f, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, initRPC, `{"jsonrpc":"2.0","method":"tools/call","params":{"name":"chat_send","arguments":{"message_id":"x","text":"no"}}}`)
	if got[0]["error"] == nil || f.calls != 0 || len(got) != 2 {
		t.Fatal(got)
	}
}
func TestLengthBounds(t *testing.T) {
	f := &fakeBackend{}
	p, _ := json.Marshal(map[string]any{"name": "chat_send", "arguments": map[string]any{"message_id": "x", "text": strings.Repeat("x", 4097)}})
	got := runRPC(t, f, initRPC, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":`+string(p)+`}`)
	if got[1]["result"].(map[string]any)["isError"] != true || f.calls != 0 {
		t.Fatal(got)
	}
	var out bytes.Buffer
	if (&Server{Backend: f}).Serve(context.Background(), strings.NewReader(strings.Repeat("x", MaxRequestBytes+1)), &out) == nil {
		t.Fatal("oversized request accepted")
	}
}

func TestCallMetadataAcceptedAndNotForwarded(t *testing.T) {
	f := &fakeBackend{}
	got := runRPC(t, f, initRPC, `{"jsonrpc":"2.0","id":"meta","method":"tools/call","params":{"name":"chat_send","_meta":{"progressToken":"progress-1"},"arguments":{"message_id":"meta-uuid","text":"hello"}}}`)
	if got[1]["error"] != nil || got[1]["result"].(map[string]any)["isError"] != false || f.calls != 1 {
		t.Fatal(got)
	}
}
func TestCallMetadataCannotOverrideArguments(t *testing.T) {
	f := &fakeBackend{}
	got := runRPC(t, f, initRPC, `{"jsonrpc":"2.0","id":"meta","method":"tools/call","params":{"name":"chat_send","_meta":{"room_id":"other"},"arguments":{"message_id":"meta-uuid","text":"hello","room_id":"other"}}}`)
	if got[1]["result"].(map[string]any)["isError"] != true || f.calls != 0 {
		t.Fatal(got)
	}
}
