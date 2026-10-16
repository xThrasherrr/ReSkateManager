package instance

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/logparse"
)

// Throwdown lines reach OnThrowdown with their players' SteamIDs: those on,
// and those who left this run, whose names the server still uses. A name two
// players had is nobody in particular, and a new run forgets who left.
func TestThrowdownPlayers(t *testing.T) {
	in := New(fakeServer(t), slog.New(slog.DiscardHandler))
	got := make(chan logparse.Throwdown, 4)
	in.OnThrowdown = func(td logparse.Throwdown) { got <- td }
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		in.Stop(ctx)
	})
	start := func() {
		t.Helper()
		if err := in.Start(); err != nil {
			t.Fatal(err)
		}
		waitState(t, in, Running)
	}
	run := func(cmds ...string) {
		t.Helper()
		for _, c := range cmds {
			if _, err := in.Command(context.Background(), c, "test"); err != nil {
				t.Fatalf("%s: %v", c, err)
			}
		}
	}
	next := func() logparse.Throwdown {
		t.Helper()
		select {
		case td := <-got:
			return td
		case <-time.After(5 * time.Second):
			t.Fatal("no throwdown")
			return logparse.Throwdown{}
		}
	}
	const zee, bob, sam1, sam2 = "76561198000000001", "76561198000000002", "76561198000000003", "76561198000000004"
	start()
	run("join "+zee+" Zee", "join "+bob+" Bob", "join "+sam1+" Sam", "leave "+bob, "leave "+sam1, "join "+sam2+" Sam")
	run("emit [throwdown] Zee's Jam has finished: 1. Bob 1,500, 2. Zee 300, 3. Sam 10")
	td := next()
	if td.Kind != logparse.ThrowdownFinished || td.Leader.ID != zee || len(td.Results) != 3 ||
		td.Results[0].ID != bob || td.Results[1].ID != zee || td.Results[2].ID != "" {
		t.Errorf("results %+v", td)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := in.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	start()
	run("emit [throwdown] Bob quit Zee's Jam")
	if td := next(); td.Kind != logparse.ThrowdownQuit || td.Player.Name != "Bob" || td.Player.ID != "" {
		t.Errorf("after a restart Bob is still known: %+v", td)
	}
}
