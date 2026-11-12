// Command plan runs an explicit bounded text DAG using outbound agent clients.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jiejhong/GooseCab/internal/orchestrator"
)

func main() { os.Exit(run()) }
func run() int {
	file := flag.String("plan", "", "JSON plan file (required)")
	state := flag.String("state", "", "persistent local journal (required)")
	action := flag.String("action", "status", "run, status, events, or reconcile (never resubmits)")
	timeout := flag.Duration("timeout", 30*time.Second, "per-task result wait")
	flag.Parse()
	if *file == "" || *state == "" || *timeout <= 0 {
		fmt.Fprintln(os.Stderr, "-plan, -state and positive -timeout required")
		return 2
	}
	if *action != "run" && *action != "status" && *action != "events" && *action != "reconcile" {
		fmt.Fprintln(os.Stderr, "invalid action")
		return 2
	}
	f, e := os.Open(*file)
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		return 1
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 1<<20))
	d.DisallowUnknownFields()
	var p orchestrator.Plan
	if e = d.Decode(&p); e == nil {
		var extra any
		if x := d.Decode(&extra); x != io.EOF {
			e = fmt.Errorf("plan must contain exactly one JSON object")
		}
	}
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		return 2
	}
	j, e := orchestrator.Open(*state, p)
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		return 1
	}
	defer j.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	o := orchestrator.Options{Token: os.Getenv("GOOSECAB_AGENT_TOKEN"), Timeout: *timeout, OnState: func(s orchestrator.State) { _ = json.NewEncoder(os.Stderr).Encode(s) }}
	var result any
	switch *action {
	case "run":
		e = j.Run(ctx, o)
		result = j.States()
	case "reconcile":
		e = j.Reconcile(ctx, o)
		result = j.States()
	case "events":
		result, e = j.Events()
	default:
		result = j.States()
	}
	if x := json.NewEncoder(os.Stdout).Encode(result); x != nil {
		fmt.Fprintln(os.Stderr, x)
		return 1
	}
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		return 1
	}
	return 0
}
