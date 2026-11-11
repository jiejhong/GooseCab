package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/jiejhong/GooseCab/internal/relay"
)

// relayConfigFromEnv rejects typos rather than silently changing the data limit.
// Limits above the client protocol's current ceiling require a coordinated upgrade.
func relayConfigFromEnv() (relay.Config, error) {
	cfg := relay.Config{MaxSize: relay.DefaultMaxSize}
	value, ok := os.LookupEnv("GOOSECAB_RELAY_MAX_BYTES")
	if !ok {
		return cfg, nil
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 1 || n > relay.DefaultMaxSize {
		return relay.Config{}, fmt.Errorf("GOOSECAB_RELAY_MAX_BYTES must be decimal bytes from 1 through %d", relay.DefaultMaxSize)
	}
	// Accept digits only: no signs, spaces, suffixes, or alternate-base syntax.
	for _, r := range value {
		if r < '0' || r > '9' {
			return relay.Config{}, fmt.Errorf("GOOSECAB_RELAY_MAX_BYTES must contain decimal digits only")
		}
	}
	cfg.MaxSize = n
	return cfg, nil
}
