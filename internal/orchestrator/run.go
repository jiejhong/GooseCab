package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jiejhong/GooseCab/internal/agent"
	"github.com/jiejhong/GooseCab/internal/protocol"
)

type Options struct {
	Token   string
	Timeout time.Duration
	OnState func(State)
}
type outcome struct{ state State }

func validResult(r protocol.CapabilityResult, id string) bool {
	return r.RequestID == id && ((r.Status == "failed" && !r.ResultAvailable && r.Output == nil) || (r.Status == "completed" && r.ResultAvailable && r.Output != nil && r.Output.MediaType == "text/plain" && utf8.ValidString(r.Output.Text) && len(r.Output.Text) <= agent.MaxTextBytes))
}
func (j *Journal) record(s State, o Options) error {
	if e := j.save(s); e != nil {
		return e
	}
	if o.OnState != nil {
		o.OnState(s)
	}
	return nil
}

// Run schedules pending tasks only. Unknown tasks and their dependents wait
// for explicit read-only Reconcile. A timeout is not task cancellation.
// A Journal must not be used concurrently by multiple goroutines.
func (j *Journal) Run(ctx context.Context, o Options) error {
	if o.Timeout <= 0 {
		return fmt.Errorf("positive per-task timeout required")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan outcome, j.Plan.Concurrency)
	active := 0
	var storageErr error
	for {
		for _, t := range j.Plan.Tasks {
			if storageErr != nil || ctx.Err() != nil || active >= j.Plan.Concurrency {
				break
			}
			s := j.states[t.ID]
			if s.Status != "pending" {
				continue
			}
			ready := true
			blocked := false
			parts := []string{t.Text}
			for _, d := range t.DependsOn {
				ds := j.states[d]
				if ds.Status == "failed" || ds.Status == "blocked" {
					blocked = true
				}
				if ds.Status != "completed" {
					ready = false
				} else if ds.Result == nil || ds.Result.Status != "completed" || !validResult(*ds.Result, ds.RequestID) {
					storageErr = fmt.Errorf("invalid dependency result in journal")
					cancel()
					ready = false
					break
				} else {
					parts = append(parts, ds.Result.Output.Text)
				}
			}
			if storageErr != nil {
				break
			}
			if blocked {
				s.Status = "blocked"
				s.Error = "Dependency failed"
				if e := j.record(s, o); e != nil {
					storageErr = e
					cancel()
				}
				continue
			}
			if !ready {
				continue
			}
			s.Input = strings.Join(parts, j.Plan.Separator)
			if !utf8.ValidString(s.Input) || len(s.Input) > agent.MaxTextBytes {
				s.Input = ""
				s.Status = "failed"
				s.Error = "Resolved input is invalid UTF-8 or exceeds 4096 bytes; nothing submitted"
				if e := j.record(s, o); e != nil {
					storageErr = e
					cancel()
				}
				continue
			}
			s.Status = "running"
			if e := j.record(s, o); e != nil {
				storageErr = e
				cancel()
				break
			}
			active++
			go func(s State, capability string) {
				callctx, stop := context.WithTimeout(ctx, o.Timeout)
				defer stop()
				c, e := agent.DialWithTokenContext(callctx, j.Plan.URL, j.Plan.Room, j.Plan.Caller, nil, o.Token)
				var r protocol.CapabilityResult
				if e == nil {
					r, e = c.InvokeTextCapability(callctx, s.RequestID, capability, s.Target, s.Input, nil)
					c.Close()
				}
				if e != nil {
					s.Status = "unknown"
					s.Error = e.Error()
				} else if !validResult(r, s.RequestID) {
					s.Status = "unknown"
					s.Error = "No authoritative terminal text result"
				} else {
					s.Status = r.Status
					s.Result = &r
					s.Error = r.Error
				}
				done <- outcome{s}
			}(s, t.CapabilityID())
		}
		if active == 0 {
			break
		}
		result := <-done
		active--
		if storageErr == nil {
			if e := j.record(result.state, o); e != nil {
				storageErr = e
				cancel()
			}
		}
	}
	if storageErr != nil {
		return storageErr
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	for _, s := range j.states {
		if s.Status != "completed" {
			return fmt.Errorf("plan incomplete: inspect status; unknown work must not be retried")
		}
	}
	return nil
}

// Reconcile fetches terminal history for unknown tasks without resubmission.
// Run must be called explicitly afterwards to launch newly ready dependents.
func (j *Journal) Reconcile(ctx context.Context, o Options) error {
	if o.Timeout <= 0 {
		return fmt.Errorf("positive per-task timeout required")
	}
	for _, t := range j.Plan.Tasks {
		s := j.states[t.ID]
		if s.Status != "unknown" {
			continue
		}
		if e := ctx.Err(); e != nil {
			return e
		}
		callctx, stop := context.WithTimeout(ctx, o.Timeout)
		c, e := agent.DialWithTokenContext(callctx, j.Plan.URL, j.Plan.Room, j.Plan.Caller, nil, o.Token)
		stop()
		if e != nil {
			return e
		}
		r, found, e := c.RecoverTextCapability(s.RequestID, t.CapabilityID(), s.Target, s.Input)
		c.Close()
		if e != nil {
			return e
		}
		if !found {
			continue
		}
		if !validResult(r, s.RequestID) {
			return fmt.Errorf("invalid recovered result for %s", s.ID)
		}
		s.Status = r.Status
		s.Result = &r
		s.Error = r.Error
		if e = j.record(s, o); e != nil {
			return e
		}
	}
	return nil
}
