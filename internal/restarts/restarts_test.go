package restarts

import (
	"context"
	"log/slog"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/instance"
	"github.com/xThrasherrr/ReSkateManager/internal/logparse"
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

// A player who joins hears how often the server restarts and how long until
// the next one, rounded up to the minute.
func TestNotice(t *testing.T) {
	now := time.Date(2026, 10, 5, 1, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		d    instance.Def
		left time.Duration
		want string
	}{
		{instance.Def{RestartHours: 6}, 2*time.Hour + 12*time.Minute + 30*time.Second, "This server restarts every 6 hours. Next restart in 2 hours 13 minutes."},
		{instance.Def{RestartHours: 1}, 59 * time.Minute, "This server restarts every hour. Next restart in 59 minutes."},
		{instance.Def{RestartHours: 2}, time.Hour + time.Minute, "This server restarts every 2 hours. Next restart in 1 hour 1 minute."},
		{instance.Def{RestartHours: 12}, 3 * time.Hour, "This server restarts every 12 hours. Next restart in 3 hours."},
		{instance.Def{RestartHours: 24}, 20 * time.Second, "This server restarts every day. Next restart in 1 minute."},
		{instance.Def{RestartHours: 168}, 6*24*time.Hour + 5*time.Hour + 30*time.Minute, "This server restarts every 7 days. Next restart in 6 days 6 hours."},
		{instance.Def{RestartTimes: []string{"04:00"}}, 3 * time.Hour, "This server restarts once a day. Next restart in 3 hours."},
		{instance.Def{RestartTimes: []string{"04:00", "16:00"}}, 3 * time.Hour, "This server restarts twice a day. Next restart in 3 hours."},
		{instance.Def{RestartTimes: []string{"00:00", "06:00", "12:00"}}, 5 * time.Hour, "This server restarts 3 times a day. Next restart in 5 hours."},
		{instance.Def{RestartTimes: []string{"04:00"}, RestartHours: 2}, 2 * time.Hour, "This server restarts on a schedule. Next restart in 2 hours."},
	} {
		if got := Notice(c.d, now.Add(c.left), now); got != c.want {
			t.Errorf("%+v, %v left: %q, want %q", c.d, c.left, got, c.want)
		}
	}
}

// Tell sends the notice to the player alone, and only on a server that
// restarts on a schedule.
func TestTell(t *testing.T) {
	ctx := context.Background()
	def := instance.Def{ID: "srv", Name: "Srv", Dir: t.TempDir()}
	if out, err := exec.Command("go", "build", "-o", def.Exe(), "../instance/testdata/fakeserver").CombinedOutput(); err != nil {
		t.Fatalf("build fake server: %v: %s", err, out)
	}
	in := instance.New(def, slog.New(slog.DiscardHandler))
	if err := in.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		in.Stop(ctx)
	})
	for deadline := time.Now().Add(10 * time.Second); in.State() != instance.Running; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("state %s", in.State())
		}
	}
	alice := instance.Player{ID: "76561198000000001", Name: "Alice"}
	if _, err := in.Command(ctx, "join "+alice.ID+" "+alice.Name, "test"); err != nil {
		t.Fatal(err)
	}
	sent := func() []string {
		var out []string
		for _, e := range in.Console() {
			if e.Kind == logparse.KindInput && strings.HasPrefix(e.Text, "msg ") {
				out = append(out, e.Text)
			}
		}
		return out
	}

	if err := Tell(ctx, in, alice); err != nil || len(sent()) != 0 {
		t.Fatalf("with no schedule: %v, sent %q", err, sent())
	}
	def.RestartHours = 6
	in.SetDef(def)
	if err := Tell(ctx, in, alice); err != nil {
		t.Fatal(err)
	}
	want := "msg " + alice.ID + " This server restarts every 6 hours. Next restart in 6 hours."
	if got := sent(); len(got) != 1 || got[0] != want {
		t.Fatalf("sent %q, want %q", got, want)
	}
}
