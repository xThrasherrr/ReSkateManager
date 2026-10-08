package instance

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/logparse"
)

// A line that reads like startup's, once the server is up, is something a
// player wrote (a name in a reply, say), and changes nothing.
func TestLateStartupLinesAreIgnored(t *testing.T) {
	in := New(Def{ID: "x", Name: "X", Dir: t.TempDir()}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	in.mu.Lock()
	defer in.mu.Unlock()
	in.state = Running
	in.readyAt = time.Now().Add(-time.Minute)
	in.info.JoinCode = "REAL"
	in.players = map[string]*Player{"76561198000000001": {ID: "76561198000000001", Name: "Alice"}}
	for _, text := range []string{
		"Z is up on Isle of Grom for 8 players.",
		"Join code: FAKE",
		"Steam ID 1, public IP 6.6.6.6.",
	} {
		e := logparse.Entry{Text: text}
		logparse.Classify(&e)
		in.handleLocked(e)
	}
	if len(in.players) != 1 || in.info.JoinCode != "REAL" || in.info.PublicIP != "" || in.readyAt.After(time.Now().Add(-30*time.Second)) {
		t.Errorf("after the lines: players %v, info %+v", in.players, in.info)
	}
}

// A stop that comes while a restart is under way stands.
func TestRestartKeepsAnotherStop(t *testing.T) {
	in := New(fakeServer(t), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := in.Start(); err != nil {
		t.Fatal(err)
	}
	waitState(t, in, Running)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// Someone presses Stop the moment the restart's own stop begins.
	events, _, _, _ := in.Subscribe()
	defer in.Unsubscribe(events)
	pressed := make(chan error, 1)
	go func() {
		for e := range events {
			if e.Type == "state" && e.State != nil && e.State.State == Stopping {
				pressed <- in.Stop(ctx)
				return
			}
		}
	}()
	err := in.Restart(ctx)
	if perr := <-pressed; perr != nil {
		t.Fatal(perr)
	}
	if err == nil {
		t.Error("the restart says it restarted")
	}
	time.Sleep(300 * time.Millisecond)
	if s := in.State(); s != Stopped {
		in.Stop(ctx)
		t.Fatalf("state %s after a stop during a restart", s)
	}
}

// A crash after the server was up is never taken for a setup problem, even
// when the last line was a join by a player named like one.
func TestFatalOnlyBeforeReady(t *testing.T) {
	in := New(fakeServer(t), slog.New(slog.NewTextHandler(io.Discard, nil)))
	crashes := make(chan Crash, 1)
	in.OnCrash = func(c Crash) { crashes <- c }
	if err := in.Start(); err != nil {
		t.Fatal(err)
	}
	waitState(t, in, Running)
	if _, err := in.Command(context.Background(), "join 76561198000000003 Config problem: x", "test"); err != nil {
		t.Fatal(err)
	}
	in.mu.Lock()
	in.readyAt = time.Now().Add(-time.Hour) // up long enough to count as stable
	p := in.proc
	in.mu.Unlock()
	p.Kill()
	select {
	case c := <-crashes:
		if c.Fatal {
			t.Errorf("a crash after the server was up was taken for a setup problem: %+v", c)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no crash reported")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	in.Stop(ctx)
}
