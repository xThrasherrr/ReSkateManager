// Package restarts restarts servers on their schedules, warning their players
// in chat first.
package restarts

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/instance"
)

const tick = 10 * time.Second

// Warnings are how long before a scheduled restart players are told, longest
// first. The longest is instance.RestartWarning.
var Warnings = []time.Duration{10 * time.Minute, 5 * time.Minute, time.Minute}

// missed is how late a restart may still happen. One found later than that,
// such as on a schedule added to a server up for days, waits for the next.
const missed = time.Minute

// Scheduler restarts each server on its schedule while Run runs.
type Scheduler struct {
	Reg *instance.Registry
	Log *slog.Logger
	// OnRestart records a scheduled restart that was due at due; err is why
	// it failed.
	OnRestart func(in *instance.Instance, due time.Time, err error)
	runs      map[string]*run // by instance id
	work      sync.WaitGroup  // restarts under way
}

// Run restarts servers as they are due until ctx ends, then waits for the
// restarts under way, which ctx also cuts short.
func (s *Scheduler) Run(ctx context.Context) {
	defer s.work.Wait()
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			s.tick(ctx, now)
		}
	}
}

func (s *Scheduler) tick(ctx context.Context, now time.Time) {
	next := map[string]*run{}
	for _, in := range s.Reg.List() {
		v := in.View()
		if v.State != instance.Running || v.StartedAt == 0 {
			continue
		}
		started := time.UnixMilli(v.StartedAt)
		r := s.runs[in.ID()]
		if r == nil || !r.started.Equal(started) {
			r = &run{started: started, handled: started}
		}
		next[in.ID()] = r
		say, restart, due := r.step(in.Def(), now, v.Players)
		if say != "" {
			cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			if _, err := in.Command(cctx, "say "+say, "schedule"); err != nil && ctx.Err() == nil {
				s.Log.Warn("restart warning", "instance", in.ID(), "err", err)
			}
			cancel()
		}
		if restart {
			s.work.Go(func() { s.restart(ctx, in, due) })
		}
	}
	s.runs = next
}

func (s *Scheduler) restart(ctx context.Context, in *instance.Instance, due time.Time) {
	in.Note("Restarting on schedule.")
	s.Log.Info("scheduled restart", "instance", in.ID())
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	err := in.Restart(ctx)
	if err != nil {
		s.Log.Warn("scheduled restart failed", "instance", in.ID(), "err", err)
	}
	if s.OnRestart != nil {
		s.OnRestart(in, due, err)
	}
}

// run is the schedule of one run of a server: one process, from its start.
type run struct {
	started time.Time
	handled time.Time     // the last restart dealt with, or the start
	due     time.Time     // the restart the warnings below are for
	warned  time.Duration // the shortest of Warnings given for it; 0 for none
}

// step decides what one tick does: what to tell the players, if anything, and
// whether to restart now, for the restart due at due. Players hear of a
// restart at each of Warnings; nobody on, nobody told.
func (r *run) step(d instance.Def, now time.Time, players int) (say string, restart bool, due time.Time) {
	due = d.NextRestart(r.started, r.handled)
	if due.IsZero() {
		return "", false, due
	}
	if now.Sub(due) > missed {
		r.handled = now
		return r.step(d, now, players)
	}
	if !due.Equal(r.due) {
		r.due, r.warned = due, 0
	}
	left := due.Sub(now)
	if left <= 0 {
		r.handled = due
		return "", true, due
	}
	if players == 0 {
		return "", false, due
	}
	for _, w := range slices.Backward(Warnings) {
		if left > w {
			continue
		}
		if r.warned == 0 || w < r.warned {
			r.warned = w
			mins := int((left + time.Minute - 1) / time.Minute)
			if mins == 1 {
				return "The server restarts in 1 minute.", false, due
			}
			return fmt.Sprintf("The server restarts in %d minutes.", mins), false, due
		}
		break
	}
	return "", false, due
}
