package restarts

import (
	"testing"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/instance"
)

// A run steps a tick at a time towards its restart: players hear at 10, 5 and
// 1 minutes, each once, then it restarts and waits for the next one.
func TestStepWarnsThenRestarts(t *testing.T) {
	at := func(h, m, s int) time.Time { return time.Date(2026, 10, 5, h, m, s, 0, time.UTC) }
	d := instance.Def{RestartTimes: []string{"04:00"}}
	r := &run{started: at(1, 0, 0), handled: at(1, 0, 0)}
	var said []string
	var restarted []time.Time
	for now := at(3, 45, 0); now.Before(at(4, 1, 0)); now = now.Add(tick) {
		say, restart, due := r.step(d, now, 3)
		if say != "" {
			said = append(said, now.Format("15:04:05")+" "+say)
		}
		if restart {
			restarted = append(restarted, due)
		}
	}
	want := []string{
		"03:50:00 The server restarts in 10 minutes.",
		"03:55:00 The server restarts in 5 minutes.",
		"03:59:00 The server restarts in 1 minute.",
	}
	if len(said) != len(want) {
		t.Fatalf("said %q, want %q", said, want)
	}
	for i := range want {
		if said[i] != want[i] {
			t.Errorf("said %q, want %q", said[i], want[i])
		}
	}
	if len(restarted) != 1 || !restarted[0].Equal(at(4, 0, 0)) {
		t.Errorf("restarted %v, want once at 04:00", restarted)
	}
}

// Nobody on: no warnings, and the restart still happens on time.
func TestStepWithNobodyOn(t *testing.T) {
	start := time.Date(2026, 10, 5, 1, 0, 0, 0, time.UTC)
	r := &run{started: start, handled: start}
	d := instance.Def{RestartHours: 2}
	for now := start.Add(110 * time.Minute); now.Before(start.Add(2 * time.Hour)); now = now.Add(tick) {
		if say, restart, _ := r.step(d, now, 0); say != "" || restart {
			t.Fatalf("%v: said %q, restart %v", now, say, restart)
		}
	}
	if _, restart, _ := r.step(d, start.Add(2*time.Hour), 0); !restart {
		t.Error("no restart when due")
	}
}

// A player joining late hears the time left; a restart long gone, as when a
// schedule is added to a server that has run for days, is not made up for.
func TestStepLateJoinerAndMissedRestart(t *testing.T) {
	start := time.Date(2026, 10, 1, 1, 0, 0, 0, time.UTC)
	d := instance.Def{RestartTimes: []string{"04:00"}}
	r := &run{started: start, handled: start}
	now := time.Date(2026, 10, 5, 3, 57, 0, 0, time.UTC)
	say, restart, due := r.step(d, now, 1)
	if restart || say != "The server restarts in 3 minutes." || !due.Equal(time.Date(2026, 10, 5, 4, 0, 0, 0, time.UTC)) {
		t.Errorf("said %q, restart %v, due %v", say, restart, due)
	}
	if say, _, _ := r.step(d, now.Add(tick), 1); say != "" {
		t.Errorf("warned twice: %q", say)
	}
}
