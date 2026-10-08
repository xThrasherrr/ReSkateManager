// Package announce sends each server's timed chat announcements.
package announce

import (
	"context"
	"log/slog"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/instance"
	"github.com/xThrasherrr/ReSkateManager/internal/store"
)

const (
	tick = 15 * time.Second
	// Gap keeps announcements on one server apart, so ones sharing an
	// interval don't land in chat together.
	Gap = time.Minute
	// MinInterval and MaxInterval bound how often one announcement repeats.
	MinInterval = time.Minute
	MaxInterval = 7 * 24 * time.Hour
)

// ValidInterval reports whether an announcement may repeat every seconds,
// checked in seconds so a huge number can't wrap into range as a Duration.
func ValidInterval(seconds int) bool {
	return seconds >= int(MinInterval/time.Second) && seconds <= int(MaxInterval/time.Second)
}

// Scheduler sends announcements to running servers that have players on.
type Scheduler struct {
	Store *store.Store
	Reg   *instance.Registry
	Log   *slog.Logger
	clock map[string]*Clock // by instance id
}

// Run sends announcements as they come due until ctx ends.
func (s *Scheduler) Run(ctx context.Context) {
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
	all, err := s.Store.Announcements(ctx, "")
	if err != nil {
		if ctx.Err() == nil {
			s.Log.Warn("load announcements", "err", err)
		}
		return
	}
	next := map[string]*Clock{}
	for _, in := range s.Reg.List() {
		c := s.clock[in.ID()]
		if c == nil {
			c = &Clock{}
		}
		next[in.ID()] = c
		v := in.View()
		a, ok := c.Due(For(all, in.ID()), v.State == instance.Running && v.Players > 0, now)
		if !ok {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		if _, err := in.Command(cctx, "say "+a.Message, "announcements"); err != nil && ctx.Err() == nil {
			s.Log.Warn("send announcement", "instance", in.ID(), "id", a.ID, "err", err)
		}
		cancel()
	}
	s.clock = next
}

// For picks the enabled announcements that apply to one server.
func For(all []store.Announcement, instanceID string) []store.Announcement {
	var out []store.Announcement
	for _, a := range all {
		if a.Enabled && (a.Instance == instanceID || a.Instance == store.AllInstances) {
			out = append(out, a)
		}
	}
	return out
}

// Clock tracks when each of one server's announcements last went out.
type Clock struct {
	since map[int64]time.Time // by announcement id: the countdown's start
	last  time.Time           // the last announcement sent on this server
}

// Due returns the announcement to send now, if any, and counts it as sent.
// Countdowns run only while players are on: an empty server restarts them, so
// someone joining isn't greeted by every overdue message at once.
func (c *Clock) Due(list []store.Announcement, active bool, now time.Time) (store.Announcement, bool) {
	since := make(map[int64]time.Time, len(list))
	for _, a := range list {
		t, ok := c.since[a.ID]
		if !ok || !active {
			t = now // a new or edited-in announcement waits one interval
		}
		since[a.ID] = t
	}
	c.since = since
	if !active || now.Sub(c.last) < Gap {
		return store.Announcement{}, false
	}
	var pick *store.Announcement
	var pickAt time.Time
	for i, a := range list {
		at := since[a.ID].Add(time.Duration(a.Interval) * time.Second)
		if !at.After(now) && (pick == nil || at.Before(pickAt)) {
			pick, pickAt = &list[i], at
		}
	}
	if pick == nil {
		return store.Announcement{}, false
	}
	c.since[pick.ID], c.last = now, now
	return *pick, true
}
