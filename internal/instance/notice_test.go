package instance

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/logparse"
)

// OnNotice hears what happens on the server in the order it happens: state
// changes, joins and leaves, players' chat with their SteamIDs when the name
// tells, throwdowns and map loads. Replies to the console's own commands
// aren't chat.
func TestNotices(t *testing.T) {
	in := New(fakeServer(t), slog.New(slog.DiscardHandler))
	got := make(chan Notice, 64) // OnNotice must never wait
	in.OnNotice = func(n Notice) { got <- n }
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		in.Stop(ctx)
	})
	if err := in.Start(); err != nil {
		t.Fatal(err)
	}
	waitState(t, in, Running)
	const zee = "76561198000000001"
	for _, c := range []string{"join " + zee + " Zee", "emit [chat] Zee: gg", "emit [chat] Ghost: boo", "say hello",
		"emit [throwdown] Zee's Jam has finished: 1. Zee 1,300", "emit Everyone has loaded Downtown.", "leave " + zee} {
		if _, err := in.Command(context.Background(), c, "test"); err != nil {
			t.Fatalf("%s: %v", c, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := in.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	var seen []string
	for done := false; !done; {
		select {
		case n := <-got:
			s := n.Kind
			switch n.Kind {
			case NoticeState:
				s += " " + string(n.Was) + ">" + string(n.State)
			case NoticeJoined, NoticeLeft:
				s += " " + n.Player.Name + " " + n.Player.ID
			case NoticeChat:
				s += " " + n.Player.Name + " " + n.Player.ID + " " + n.Text
			case NoticeThrowdown:
				s += " " + n.Throwdown.Kind + " " + n.Throwdown.Results[0].ID
			case NoticeMap:
				s += " " + n.Map
			}
			seen = append(seen, s)
			done = s == "state stopping>stopped"
		case <-time.After(5 * time.Second):
			t.Fatalf("stopped hearing after %q", seen)
		}
	}
	want := []string{
		"state stopped>starting",
		"state starting>running",
		"joined Zee " + zee,
		"chat Zee " + zee + " gg",
		"chat Ghost  boo",
		"throwdown " + logparse.ThrowdownFinished + " " + zee,
		"map Downtown",
		"left Zee " + zee,
		"state running>stopping",
		"state stopping>stopped",
	}
	if strings.Join(seen, "|") != strings.Join(want, "|") {
		t.Fatalf("heard:\n%s\nwant:\n%s", strings.Join(seen, "\n"), strings.Join(want, "\n"))
	}
}
