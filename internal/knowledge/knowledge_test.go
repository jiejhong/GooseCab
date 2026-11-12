package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiejhong/GooseCab/internal/protocol"
	"github.com/jiejhong/GooseCab/internal/relay"
)

func openTest(t *testing.T, id string) (*Store, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "knowledge.db")
	s, e := Open(p, id)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s, p
}
func record(id string) Record {
	return Record{ID: id, Version: 1, Title: "Retry lesson", Body: "PRIVATE BODY never enter gateway events", Source: "synthetic operator note", Tags: []string{"reliability"}, ProvenanceIDs: []string{"synthetic-test-incident"}, CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z"}
}
func put(t *testing.T, s *Store, r Record) Ref {
	t.Helper()
	ref, e := s.Put(r)
	if e != nil {
		t.Fatal(e)
	}
	return ref
}
func share(t *testing.T, s *Store, ref Ref) {
	t.Helper()
	if e := s.Share(Collection{"lessons", []Ref{ref}, []string{"bob"}}); e != nil {
		t.Fatal(e)
	}
}
func invoke(s *Store, caller string) protocol.CapabilityResult {
	return s.Invoke(protocol.WithInvocationActor(context.Background(), "demo", caller), protocol.CapabilityInvoke{RequestID: "q1", CapabilityID: "cap.knowledge.lessons", Payload: map[string]any{"query": "retry"}})
}
func TestPrivateAndCallerBoundMetadata(t *testing.T) {
	a, _ := openTest(t, "alice")
	ref := put(t, a, record("retry"))
	secret := record("secret")
	secret.Title = "Unshared secret"
	put(t, a, secret)
	ids, e := a.CapabilityIDs()
	if e != nil || len(ids) != 0 {
		t.Fatal("private capability discoverable", ids, e)
	}
	if _, e = a.Export("lessons", "bob"); e == nil {
		t.Fatal("private export")
	}
	share(t, a, ref)
	if invoke(a, "mallory").Status != "failed" {
		t.Fatal("unauthorized accepted")
	}
	if r := a.Invoke(context.Background(), protocol.CapabilityInvoke{CapabilityID: "cap.knowledge.lessons", Payload: map[string]any{"caller": "bob", "query": "retry"}}); r.Status != "failed" {
		t.Fatal("payload spoofed identity")
	}
	r := invoke(a, "bob")
	if r.Status != "completed" || r.Output == nil {
		t.Fatal(r)
	}
	if strings.Contains(r.Output.Text, "PRIVATE") || strings.Contains(r.Output.Text, "secret") || strings.Contains(r.Output.Text, "body") {
		t.Fatal("private data leaked", r)
	}
	// Changes to local records do not broaden old approvals to a new version.
	v2 := record("retry")
	v2.Version = 2
	v2.Body = "new version private"
	put(t, a, v2)
	b, e := a.Export("lessons", "bob")
	if e != nil {
		t.Fatal(e)
	}
	v, e := Inspect(bytes.NewReader(b))
	if e != nil || v.Package.Records[0].Version != 1 {
		t.Fatal("share expanded", e)
	}
	if e = a.Unshare("lessons"); e != nil {
		t.Fatal(e)
	}
	if invoke(a, "bob").Status != "failed" {
		t.Fatal("revocation ignored")
	}
}
func TestRelayStageReviewImportOffline(t *testing.T) {
	a, _ := openTest(t, "alice")
	b, path := openTest(t, "bob")
	ref := put(t, a, record("retry"))
	share(t, a, ref)
	raw, e := a.Export("lessons", "bob")
	if e != nil {
		t.Fatal(e)
	}
	srv := httptest.NewServer(relay.New(relay.Config{MaxSize: MaxPackageBytes}))
	defer srv.Close()
	client := relay.Client{URL: srv.URL}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ticket, e := client.Create(ctx, "alice", "bob", int64(len(raw)), Digest(raw))
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { done <- client.Send(ctx, ticket, bytes.NewReader(raw)) }()
	staged, e := client.ReceiveFile(ctx, ticket, t.TempDir(), "review.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	records, e := b.Search("")
	if e != nil || len(records) != 0 {
		t.Fatal("staging imported automatically", e)
	}
	received, e := os.ReadFile(staged)
	if e != nil {
		t.Fatal(e)
	}
	review, e := Inspect(bytes.NewReader(received))
	if e != nil {
		t.Fatal(e)
	}
	if e = b.Import(received, ""); e == nil {
		t.Fatal("implicit approval")
	}
	if e = b.Import(received, review.Digest); e != nil {
		t.Fatal(e)
	}
	if e = b.Import(received, review.Digest); e != nil {
		t.Fatal("replay not idempotent", e)
	}
	ids, e := b.CapabilityIDs()
	if e != nil || len(ids) != 0 {
		t.Fatal("approval transferred", ids, e)
	}
	if _, e = b.Export("lessons", "alice"); e == nil {
		t.Fatal("implicit resharing")
	}
	a.Close()
	srv.Close()
	b.Close()
	offline, e := Open(path, "bob")
	if e != nil {
		t.Fatal(e)
	}
	defer offline.Close()
	records, e = offline.Search("PRIVATE BODY")
	if e != nil || len(records) != 1 {
		t.Fatal(records, e)
	}
	if records[0].Origin != "alice" || records[0].Source != "synthetic operator note" || records[0].ProvenanceIDs[0] != "synthetic-test-incident" {
		t.Fatal("provenance lost")
	}
}
func TestTamperingConflictsAndRecipient(t *testing.T) {
	a, _ := openTest(t, "alice")
	b, _ := openTest(t, "bob")
	c, _ := openTest(t, "carol")
	ref := put(t, a, record("retry"))
	share(t, a, ref)
	raw, e := a.Export("lessons", "bob")
	if e != nil {
		t.Fatal(e)
	}
	if e = c.Import(raw, Digest(raw)); e == nil {
		t.Fatal("recipient approval transferable")
	}
	tampered := bytes.Replace(raw, []byte("Retry lesson"), []byte("Changed title"), 1)
	if e = b.Import(tampered, Digest(raw)); e == nil {
		t.Fatal("tampered digest accepted")
	}
	if e = b.Import(raw, Digest(raw)); e != nil {
		t.Fatal(e)
	}
	if e = b.Import(tampered, Digest(tampered)); e == nil {
		t.Fatal("same version conflict accepted")
	}
	v2 := record("retry")
	v2.Version = 2
	v2.Body = "New private version"
	ref2 := put(t, a, v2)
	share(t, a, ref2)
	raw2, e := a.Export("lessons", "bob")
	if e != nil {
		t.Fatal(e)
	}
	if e = b.Import(raw2, Digest(raw2)); e != nil {
		t.Fatal(e)
	}
	records, e := b.Search("")
	if e != nil || len(records) != 1 || records[0].Version != 2 {
		t.Fatal("version handling failed", records, e)
	}
	// Existing exact old replay remains a no-op, never reactivating old records.
	if e = b.Import(raw, Digest(raw)); e != nil {
		t.Fatal(e)
	}
	records, _ = b.Search("")
	if records[0].Version != 2 {
		t.Fatal("replay rollback")
	}
	review, _ := Inspect(bytes.NewReader(raw))
	review.Package.Records[0].Origin = "bob"
	spoof, _ := json.Marshal(review.Package)
	if e = b.Import(spoof, Digest(spoof)); e == nil {
		t.Fatal("remote authored local origin")
	}
}
func TestBoundsAndSelection(t *testing.T) {
	a, path := openTest(t, "alice")
	if other, e := Open(path, "bob"); e == nil {
		other.Close()
		t.Fatal("owner mismatch")
	}
	r := record("large")
	r.Body = strings.Repeat("x", MaxRecordBytes)
	if _, e := a.Put(r); e == nil {
		t.Fatal("oversized record")
	}
	ref := put(t, a, record("retry"))
	bad := ref
	bad.Digest = "bad"
	if e := a.Share(Collection{"lessons", []Ref{bad}, []string{"bob"}}); e == nil {
		t.Fatal("unverified selection")
	}
	if e := a.Share(Collection{"lessons", []Ref{ref}, []string{"*"}}); e == nil {
		t.Fatal("wildcard caller")
	}
	if _, e := Inspect(strings.NewReader(strings.Repeat("x", MaxPackageBytes+1))); e == nil {
		t.Fatal("oversized package")
	}
}

func TestRemoteResultBoundsAndNoSource(t *testing.T) {
	a, _ := openTest(t, "alice")
	refs := []Ref{}
	for i := 0; i < 20; i++ {
		r := record(string(rune('a' + i)))
		r.Title = strings.Repeat("<", 256)
		r.Source = "https://user:password@example.invalid/private?secret=value"
		refs = append(refs, put(t, a, r))
	}
	if e := a.Share(Collection{"lessons", refs, []string{"bob"}}); e != nil {
		t.Fatal(e)
	}
	r := a.Invoke(protocol.WithInvocationActor(context.Background(), "room", "bob"), protocol.CapabilityInvoke{RequestID: "bounds", CapabilityID: "cap.knowledge.lessons", Payload: map[string]any{"query": ""}})
	if r.Status != "completed" || r.Output == nil || len(r.Output.Text) > 4096 || r.Output.MediaType != "text/plain" {
		t.Fatal("invalid result bounds", r)
	}
	if strings.Contains(r.Output.Text, "password") || strings.Contains(r.Output.Text, "secret=value") || !strings.Contains(r.Output.Text, `"truncated":true`) {
		t.Fatal("raw source leak or missing truncation", r.Output.Text)
	}
	for _, payload := range []any{nil, map[string]any{}, map[string]any{"query": nil}, map[string]any{"query": "", "caller": "bob"}} {
		r = a.Invoke(protocol.WithInvocationActor(context.Background(), "room", "bob"), protocol.CapabilityInvoke{CapabilityID: "cap.knowledge.lessons", Payload: payload})
		if r.Status != "failed" {
			t.Fatal("invalid payload accepted")
		}
	}
}

func TestCascadeExactCallerIdentity(t *testing.T) {
	a, _ := openTest(t, "alice")
	ref := put(t, a, record("retry"))
	if e := a.Share(Collection{"lessons", []Ref{ref}, []string{"edge/bridge"}}); e != nil {
		t.Fatal("valid namespaced bridge caller rejected", e)
	}
	if r := invoke(a, "edge/bridge"); r.Status != "completed" {
		t.Fatal("actual bridge actor not authorized", r)
	}
	if r := invoke(a, "bridge"); r.Status != "failed" {
		t.Fatal("unqualified identity bypassed exact ACL")
	}
	spoofed := a.Invoke(protocol.WithInvocationActor(context.Background(), "room", "mallory"), protocol.CapabilityInvoke{CapabilityID: "cap.knowledge.lessons", Payload: map[string]any{"query": "retry", "caller": "edge/bridge", "original_caller": "edge/bridge"}})
	if spoofed.Status != "failed" {
		t.Fatal("payload claimed actor bypassed ACL")
	}
	for _, caller := range []string{"", "*", "edge/*", "edge/?", "edge/[bridge]", "edge/bridge\n", "edge/ bridge", strings.Repeat("a", 129), string([]byte{0xff})} {
		if e := a.Share(Collection{"lessons", []Ref{ref}, []string{caller}}); e == nil {
			t.Fatalf("unsafe caller accepted: %q", caller)
		}
	}
	bad := record("edge/record")
	if _, e := a.Put(bad); e == nil {
		t.Fatal("record identity grammar widened")
	}
}
