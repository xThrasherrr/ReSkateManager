// Package restarts restarts servers on their schedules, warning their players
// in chat first.
package restarts

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
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

// Tell lets a player who just joined know when the server restarts, in a
// message to them alone, if it restarts on a schedule. The server sends its
// welcome before it logs the join, so this lands after it.
func Tell(ctx context.Context, in *instance.Instance, p instance.Player) error {
	v := in.View()
	if v.State != instance.Running || v.NextRestart == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err := in.Command(ctx, "msg "+p.ID+" "+Notice(v.Def, time.UnixMilli(v.NextRestart), time.Now()), "schedule")
	return err
}

// Notice is what Tell says: how often the server restarts, and how long
// until the next restart, due at next.
func Notice(d instance.Def, next, now time.Time) string {
	every := func(n int, unit string) string {
		if n == 1 {
			return "every " + unit
		}
		return "every " + plural(n, unit)
	}
	var how string
	switch n, h := len(d.RestartTimes), d.RestartHours; {
	case h > 0 && n > 0:
		how = "on a schedule"
	case h > 0 && h%24 == 0:
		how = every(h/24, "day")
	case h > 0:
		how = every(h, "hour")
	case n == 1:
		how = "once a day"
	case n == 2:
		how = "twice a day"
	default:
		how = fmt.Sprintf("%d times a day", n)
	}
	return fmt.Sprintf("This server restarts %s. Next restart in %s.", how, span(next.Sub(now)))
}

// span is d rounded up to the minute, as "2 hours 13 minutes"; past a day,
// up to the hour, as "6 days 6 hours".
func span(d time.Duration) string {
	type part struct {
		n    int
		unit string
	}
	mins := max(int((d+time.Minute-1)/time.Minute), 1)
	units := []part{{mins / 60, "hour"}, {mins % 60, "minute"}}
	if mins > 24*60 {
		hours := (mins + 59) / 60
		units = []part{{hours / 24, "day"}, {hours % 24, "hour"}}
	}
	var parts []string
	for _, u := range units {
		if u.n > 0 {
			parts = append(parts, plural(u.n, u.unit))
		}
	}
	return strings.Join(parts, " ")
}

// plural is n of unit, as "1 hour" or "6 hours".
func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}
