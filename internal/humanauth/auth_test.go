package humanauth

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func syntheticToken(fill byte) string {
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{fill}, tokenBytes))
}

func credential(id, token string, expiry time.Time, rooms map[string]RoomGrants) Credential {
	digest := sha256.Sum256([]byte(token))
	return Credential{HumanID: id, TokenSHA256: hex.EncodeToString(digest[:]), ExpiresAt: expiry, Rooms: rooms}
}

func bearer(token string) []string {
	return []string{"Bearer " + token}
}

func TestAuthorizationChecksIdentityExpiryAndExactRoomGrants(t *testing.T) {
	now := time.Date(2030, 1, 1, 10, 0, 0, 0, time.UTC)
	shared, observer := syntheticToken(1), syntheticToken(2)
	reader, writer, expired, noGrants, both := syntheticToken(3), syntheticToken(4), syntheticToken(5), syntheticToken(6), syntheticToken(7)
	reserved := ReservedDigests{Shared: sha256.Sum256([]byte(shared)), Observer: sha256.Sum256([]byte(observer))}
	a, err := New(Config{Enabled: true, Credentials: []Credential{
		credential("human-reader", reader, now.Add(time.Hour), map[string]RoomGrants{"room one": {Read: true}}),
		credential("human-writer", writer, now.Add(time.Hour), map[string]RoomGrants{"room one": {Write: true}}),
		credential("human-expired", expired, now, map[string]RoomGrants{"room one": {Read: true, Write: true}}),
		credential("human-no-grants", noGrants, now.Add(time.Hour), nil),
		credential("human-both", both, now.Add(time.Hour), map[string]RoomGrants{"room two": {Read: true, Write: true}}),
	}}, reserved)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		header     []string
		room       string
		permission Permission
		expectedID string
	}{
		{"reader", bearer(reader), "room one", ReadRoom, "human-reader"},
		{"reader cannot write", bearer(reader), "room one", WriteRoom, ""},
		{"writer", bearer(writer), "room one", WriteRoom, "human-writer"},
		{"writer has no implicit read", bearer(writer), "room one", ReadRoom, ""},
		{"both read", bearer(both), "room two", ReadRoom, "human-both"},
		{"both write", bearer(both), "room two", WriteRoom, "human-both"},
		{"other room", bearer(reader), "room two", ReadRoom, ""},
		{"room ID is exact", bearer(reader), "Room one", ReadRoom, ""},
		{"blank room", bearer(reader), "", ReadRoom, ""},
		{"expired at boundary", bearer(expired), "room one", ReadRoom, ""},
		{"no room grants", bearer(noGrants), "room one", ReadRoom, ""},
		{"unknown permission", bearer(both), "room two", "broadcast_all", ""},
		{"unknown credential", bearer(syntheticToken(8)), "room one", ReadRoom, ""},
		{"shared bearer", bearer(shared), "room one", ReadRoom, ""},
		{"observer bearer", bearer(observer), "room one", WriteRoom, ""},
		{"missing header", nil, "room one", ReadRoom, ""},
		{"duplicate header", []string{"Bearer " + reader, "Bearer " + reader}, "room one", ReadRoom, ""},
		{"combined header", []string{"Bearer " + reader + ", Bearer " + reader}, "room one", ReadRoom, ""},
		{"wrong scheme", []string{"Basic " + reader}, "room one", ReadRoom, ""},
		{"Bearer is case insensitive", []string{"bearer " + reader}, "room one", ReadRoom, "human-reader"},
		{"short token", bearer("fixture"), "room one", ReadRoom, ""},
		{"invalid encoding", bearer(strings.Repeat("!", len(reader))), "room one", ReadRoom, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			principal, ok := a.authorizeAt(test.header, test.room, test.permission, now)
			if ok != (test.expectedID != "") || principal.HumanID != test.expectedID {
				t.Fatal("authorization outcome or identity differed from the configured result")
			}
		})
	}
}

func TestDisabledPolicyAndConfigurationCopy(t *testing.T) {
	token := syntheticToken(9)
	grants := map[string]RoomGrants{"demo": {Read: true}}
	cfg := Config{Credentials: []Credential{credential("human", token, time.Now().Add(time.Hour), grants)}}
	disabled, err := New(cfg, ReservedDigests{})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []*Authorizer{nil, {}, disabled} {
		if principal, ok := a.Authorize(bearer(token), "demo", ReadRoom); ok || principal != (Principal{}) {
			t.Fatal("default or disabled policy granted access")
		}
	}
	cfg.Enabled = true
	a, err := New(cfg, ReservedDigests{})
	if err != nil {
		t.Fatal(err)
	}
	grants["demo"] = RoomGrants{Write: true}
	cfg.Credentials[0].HumanID = "changed"
	if principal, ok := a.Authorize(bearer(token), "demo", ReadRoom); !ok || principal.HumanID != "human" {
		t.Fatal("config mutation changed the verified identity or read grant")
	}
	if principal, ok := a.Authorize(bearer(token), "demo", WriteRoom); ok || principal != (Principal{}) {
		t.Fatal("config mutation granted write access")
	}
}

func TestInvalidConfigurationAndNoCredentialDisclosure(t *testing.T) {
	token, legacy, observer := syntheticToken(10), syntheticToken(11), syntheticToken(12)
	expiry := time.Now().Add(time.Hour)
	rooms := map[string]RoomGrants{"demo": {Read: true}}
	valid := credential("human", token, expiry, rooms)
	sharedEntry := credential("human-shared", legacy, expiry, rooms)
	observerEntry := credential("human-observer", observer, expiry, rooms)
	reserved := ReservedDigests{Shared: sha256.Sum256([]byte(legacy)), Observer: sha256.Sum256([]byte(observer))}
	for _, test := range []struct {
		name    string
		entries []Credential
	}{
		{"reject shared digest", []Credential{sharedEntry}},
		{"reject observer digest", []Credential{observerEntry}},
		{"reject duplicate digest", []Credential{valid, credential("another", token, expiry, rooms)}},
		{"reject duplicate human ID", []Credential{valid, credential("human", syntheticToken(13), expiry, rooms)}},
		{"reject missing expiry", []Credential{credential("human", token, time.Time{}, rooms)}},
		{"reject missing ID", []Credential{credential("", token, expiry, rooms)}},
		{"reject empty room", []Credential{credential("human", token, expiry, map[string]RoomGrants{" ": {Read: true}})}},
		{"reject plaintext in digest field", []Credential{{HumanID: "human", TokenSHA256: token, ExpiresAt: expiry, Rooms: rooms}}},
		{"reject malformed hex", []Credential{{HumanID: "human", TokenSHA256: strings.Repeat("z", 64), ExpiresAt: expiry, Rooms: rooms}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, err := New(Config{Enabled: true, Credentials: test.entries}, reserved)
			if err == nil || a != nil {
				t.Fatal("invalid config did not fail closed")
			}
			for _, secret := range []string{token, legacy, observer, valid.TokenSHA256, sharedEntry.TokenSHA256, observerEntry.TokenSHA256} {
				if strings.Contains(err.Error(), secret) {
					t.Fatal("validation error included credential data")
				}
			}
		})
	}
	a, err := New(Config{Enabled: true, Credentials: []Credential{valid}}, reserved)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%v %+v %#v", a, a, *a); got != strings.Repeat("humanauth.Authorizer ", 2)+"humanauth.Authorizer" {
		t.Fatal("formatting the authorizer exposed its internal state")
	}
	if got, err := json.Marshal(a); err != nil || string(got) != "{}" {
		t.Fatal("JSON encoding the authorizer exposed its internal state")
	}
}
