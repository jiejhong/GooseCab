// Command cascade runs an explicit, fail-closed two-gateway text bridge.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"github.com/jiejhong/GooseCab/internal/cascade"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	config := flag.String("config", "", "explicit bridge JSON configuration")
	state := flag.String("state", "", "persistent mapping journal; reuse on restart")
	timeout := flag.Duration("timeout", 30*time.Second, "downstream outcome deadline; timeout means unknown")
	flag.Parse()
	f, err := os.Open(*config)
	if err != nil {
		log.Fatal(err)
	}
	var c cascade.Config
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	err = d.Decode(&c)
	f.Close()
	if err != nil {
		log.Fatal(err)
	}
	c.Timeout = *timeout
	c.Upstream.Token = os.Getenv("GOOSECAB_UPSTREAM_TOKEN")
	c.Downstream.Token = os.Getenv("GOOSECAB_DOWNSTREAM_TOKEN")
	b, err := cascade.Open(c, *state)
	if err != nil {
		log.Fatal(err)
	}
	defer b.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err = b.Run(ctx); err != nil {
		log.Fatal(err)
	}
}
