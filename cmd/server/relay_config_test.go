package main

import (
	"fmt"
	"github.com/jiejhong/GooseCab/internal/store"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jiejhong/GooseCab/internal/relay"
)

func TestRelayConfigFromEnv(t *testing.T) {
	t.Setenv("GOOSECAB_RELAY_MAX_BYTES", "")
	if err := os.Unsetenv("GOOSECAB_RELAY_MAX_BYTES"); err != nil {
		t.Fatal(err)
	}
	cfg, err := relayConfigFromEnv()
	if err != nil || cfg.MaxSize != relay.DefaultMaxSize {
		t.Fatalf("default: %+v, %v", cfg, err)
	}
	for _, tc := range []struct {
		value string
		want  int64
	}{
		{"1", 1}, {"31457280", 30 << 20}, {"33554432", 32 << 20}, {"268435456", 256 << 20},
		{"", 0}, {"0", 0}, {"-1", 0}, {"+32", 0}, {"30MiB", 0}, {"0x2000000", 0}, {" 33554432", 0}, {"268435457", 0}, {"99999999999999999999999", 0},
	} {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv("GOOSECAB_RELAY_MAX_BYTES", tc.value)
			cfg, err := relayConfigFromEnv()
			if tc.want == 0 {
				if err == nil {
					t.Fatal("invalid value accepted")
				}
			} else if err != nil || cfg.MaxSize != tc.want {
				t.Fatalf("got %+v, %v", cfg, err)
			}
		})
	}
}

func TestConfiguredRelayHandlerLimit(t *testing.T) {
	t.Setenv("GOOSECAB_AGENT_TOKEN", "secret")
	t.Setenv("GOOSECAB_RELAY_MAX_BYTES", "33554432")
	cfg, err := relayConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "gateway.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	handler := newServerHandlerWithRelayConfig(db, ":8312", path, cfg)
	for _, tc := range []struct {
		size   int64
		status int
	}{{33554432, 201}, {33554433, 400}} {
		body := fmt.Sprintf(`{"sender":"a","recipient":"b","size":%d,"sha256":"%s"}`, tc.size, strings.Repeat("0", 64))
		response := request(handler, "POST", "/api/v1/relay", "Bearer secret", body)
		if response.Code != tc.status {
			t.Fatalf("size %d: HTTP %d; want %d", tc.size, response.Code, tc.status)
		}
	}
}
