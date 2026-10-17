package instance

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// cardTime sets CardUnit for one test.
func cardTime(t *testing.T, d time.Duration) {
	old := CardUnit
	t.Cleanup(func() { CardUnit = old })
	CardUnit = d
}

// Turns come once the card is down: the manager's own announcements first,
// then the rest in the order they came. One that gives up holds nobody up.
func TestCardTurns(t *testing.T) {
	cardTime(t, 20*time.Millisecond)
	var c cards
	ctx := context.Background()
	if err := c.take(ctx, false); err != nil {
		t.Fatal(err)
	}
	up := time.Now()
	c.saw("up") // 6 units
	c.done()
	type turn struct {
		name string
		at   time.Duration
	}
	order := make(chan turn, 4)
	take := func(ctx context.Context, name string, first bool) {
		go func() {
			if err := c.take(ctx, first); err != nil {
				order <- turn{name + ": " + err.Error(), 0}
				return
			}
			order <- turn{name, time.Since(up)}
			c.done()
		}()
		time.Sleep(10 * time.Millisecond) // so they queue in this order
	}
	gone, cancel := context.WithCancel(ctx)
	take(ctx, "plugin 1", false)
	take(gone, "plugin 2", false)
	take(ctx, "plugin 3", false)
	take(ctx, "manager", true)
	cancel()
	want := []string{"plugin 2: context canceled", "manager", "plugin 1", "plugin 3"}
	for i, w := range want {
		got := <-order
		if got.name != w {
			t.Errorf("turn %d: %q, want %q", i, got.name, w)
		}
		if i == 1 && got.at < 6*CardUnit {
			t.Errorf("the first turn came %s after the card went up, before its %s were over", got.at, 6*CardUnit)
		}
	}
}

// A card the server put up, for anyone, holds the next turn back; a server
// that starts again has none up.
func TestCardSeen(t *testing.T) {
	cardTime(t, 20*time.Millisecond)
	var c cards
	ctx := context.Background()
	c.saw(strings.Repeat("x", 300)) // 15 units
	start := time.Now()
	if err := c.take(ctx, true); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d < 14*CardUnit {
		t.Errorf("a turn came %s into a card the server showed for %s", d, 15*CardUnit)
	}
	c.done()
	c.saw("short")
	c.clear()
	start = time.Now()
	if err := c.take(ctx, false); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 3*CardUnit {
		t.Errorf("waited %s for a card a restart took down", d)
	}
}

// On a running server, two players who join together each see their card
// whole, after the one the console put up; one for nobody holds nothing up.
func TestAnnounce(t *testing.T) {
	cardTime(t, 50*time.Millisecond)
	in := New(fakeServer(t), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := in.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		in.Stop(ctx)
	})
	waitState(t, in, Running)
	ctx := context.Background()
	const alice, bob = "76561198000000001", "76561198000000002"
	for _, c := range []string{"join " + alice + " Alice", "join " + bob + " Bob", "announce Restart in 5 minutes"} {
		if _, err := in.Command(ctx, c, "test"); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	replies := make([]string, 3)
	for i, to := range []string{alice, bob, "76561198000000009"} {
		wg.Go(func() {
			var err error
			if replies[i], err = in.Announce(ctx, to, "Welcome back", "test", false); err != nil {
				t.Error(err)
			}
		})
		time.Sleep(20 * time.Millisecond)
	}
	wg.Wait()
	if replies[0] != "Announced to Alice." || replies[1] != "Announced to Bob." || !strings.HasPrefix(replies[2], "No single connected player") {
		t.Fatalf("replies %q", replies)
	}
	// The cards as the server put them up, and when.
	var at []int64
	for _, e := range in.Console() {
		if e.Tag == "announcement" {
			at = append(at, e.At)
		}
	}
	if len(at) != 3 {
		t.Fatalf("%d cards went up, want 3", len(at))
	}
	for i := 1; i < len(at); i++ {
		if gap := time.Duration(at[i]-at[i-1]) * time.Millisecond; gap < 6*CardUnit-20*time.Millisecond {
			t.Errorf("card %d went up %s after the one before, inside its %s", i+1, gap, 6*CardUnit)
		}
	}
	// The one for nobody waited for Bob's card to come down and put none up,
	// so the next goes up at once.
	start := time.Now()
	if _, err := in.Announce(ctx, "", "Next map: Danny Way's", "test", true); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 2*CardUnit {
		t.Errorf("an announcement waited %s for a card that never went up", d)
	}
}
