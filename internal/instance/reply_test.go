package instance

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/logparse"
)

// queue registers a waiter as send does, without a process to send to.
func queue(in *Instance, accept func(string) bool) *waiter {
	in.mu.Lock()
	defer in.mu.Unlock()
	w := &waiter{ch: make(chan logparse.Entry, 1), expires: time.Now().Add(time.Minute), accept: accept}
	in.waiters = append(in.waiters, w)
	return w
}

func got(w *waiter) (string, bool) {
	select {
	case e := <-w.ch:
		return e.Text, true
	default:
		return "", false
	}
}

func TestStrayLinesAreNotReplies(t *testing.T) {
	in := New(Def{ID: "t", Dir: t.TempDir()}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	in.asm = logparse.NewAssembler()

	w := queue(in, nil)
	for _, l := range []string{
		"[10:00:00] Server update 1.0.9 is ready; the server restarts to install it when nobody is on.",
		"[10:00:01] [vote] The vote to change the map to Isle of Grom passed (3 yes, 0 no, 3 needed).",
		"[10:00:01] Changing map to Isle of Grom",
		// A custom vote (2.0.2) runs the owner's command, named by its description.
		"[10:00:01] [vote] The vote to reload the current map passed (2 yes, 1 no, 2 needed).",
		"[10:00:01] Changing map to Isle of Grom",
		"[10:00:01] [vote] The vote to kick Eve passed (2 yes, 0 no, 2 needed).",
		"[10:00:01] Eve left (You were kicked from this server by a vote.)",
		"[10:00:02] Server renamed to Test.",
	} {
		in.onLine(l)
	}
	in.onIdle()
	if text, ok := got(w); text != "Server renamed to Test." {
		t.Errorf("reply %q (%v)", text, ok)
	}
	c := in.Console()
	for _, i := range []int{len(c) - 6, len(c) - 4} {
		if c[i].Kind != logparse.KindTagged || c[i].Tag != "vote" {
			t.Errorf("vote outcome logged as %+v", c[i])
		}
	}
	if c[len(c)-2].Kind != logparse.KindLeave {
		t.Errorf("vote kick logged as %+v", c[len(c)-2])
	}

	// An in-game admin's result can't be told from a reply by order alone, so
	// parsed replies also check their shape.
	w = queue(in, replyShape(playersReplyRe))
	for _, l := range []string{
		"[10:01:00] [admin] Bob: ban Eve",
		"[10:01:00] Eve was banned.",
		"[10:01:01] 1 players",
		"  76561198000000002  Bob  (admin)",
	} {
		in.onLine(l)
	}
	in.onIdle()
	if text, _ := got(w); text != "1 players\n  76561198000000002  Bob  (admin)" {
		t.Errorf("players reply %q", text)
	}

	w = queue(in, replyShape(mapsReplyRe))
	in.onLine(`[10:02:00] Unknown command "maps". Type help.`)
	in.onIdle()
	if _, ok := got(w); !ok {
		t.Error("a refusal did not end the wait")
	}
}

func TestNetworkSummaries(t *testing.T) {
	in := New(Def{ID: "t", Dir: t.TempDir()}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	in.asm = logparse.NewAssembler()
	got := make(chan logparse.Network, 4)
	in.OnNetwork = func(_ time.Time, n logparse.Network) { got <- n }
	line := "[10:00:00] [network] 2 players, 30 KB/s out, 8 KB/s in, worst ping 70 ms"

	in.onLine(line)
	in.onIdle()
	in.mu.Lock()
	in.state = Running
	in.mu.Unlock()
	in.onLine(line)
	in.onLine(line) // a second within the minute is not the server's
	in.onIdle()
	select {
	case n := <-got:
		if n.Players != 2 || n.PingMs != 70 {
			t.Errorf("summary %+v", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no summary while running")
	}
	select {
	case n := <-got:
		t.Errorf("another summary %+v", n)
	case <-time.After(200 * time.Millisecond):
	}
}
