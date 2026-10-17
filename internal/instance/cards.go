package instance

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

// A server shows one announcement card at a time, to everyone or to one
// player: another announce, to anyone, takes it over (Host::announce). So
// the cards the manager sends wait their turn here, the manager's own
// announcements before plugins' cards, each once the card up now is down.

// CardUnit is a second of a card's time; tests make it shorter.
var CardUnit = time.Second

// ErrCardWait is why Announce sent nothing: its turn didn't come in time.
var ErrCardWait = errors.New("no turn at the server's card")

// CardTime is how long the server keeps text up on its card, as ReSkate
// works it out (since 2.0.2): longer text stays longer.
func CardTime(text string) time.Duration {
	return time.Duration(min(max(5+len(text)/20, 6), 15)) * CardUnit
}

type cards struct {
	mu    sync.Mutex
	until time.Time // the card up now comes down then
	busy  bool      // a turn is sending its card
	queue []*cardTurn
	timer *time.Timer
}

type cardTurn struct {
	first bool          // the manager's own announcement
	ready chan struct{} // closed when its turn comes
}

// take waits for a turn at the card.
func (c *cards) take(ctx context.Context, first bool) error {
	t := &cardTurn{first: first, ready: make(chan struct{})}
	c.mu.Lock()
	c.queue = append(c.queue, t)
	c.nextLocked()
	c.mu.Unlock()
	select {
	case <-t.ready:
		return nil
	case <-ctx.Done():
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if i := slices.Index(c.queue, t); i >= 0 {
		c.queue = slices.Delete(c.queue, i, i+1)
	} else { // its turn came as it gave up: pass it on
		c.busy = false
		c.nextLocked()
	}
	return ctx.Err()
}

// done ends a turn. A card it put up holds the next one by the line the
// server logs for it, before its reply (saw).
func (c *cards) done() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.busy = false
	c.nextLocked()
}

// saw notes a card the server put up, whoever sent it.
func (c *cards) saw(text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t := time.Now().Add(CardTime(text)); t.After(c.until) {
		c.until = t
	}
}

// clear forgets the card: a server that starts again has none up.
func (c *cards) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.until = time.Time{}
	c.nextLocked()
}

// nextLocked gives the next turn once the card is down, or sets a timer for
// then.
func (c *cards) nextLocked() {
	if c.busy || len(c.queue) == 0 {
		return
	}
	if wait := time.Until(c.until); wait > 0 {
		if c.timer == nil {
			c.timer = time.AfterFunc(wait, func() {
				c.mu.Lock()
				defer c.mu.Unlock()
				c.nextLocked()
			})
		} else {
			c.timer.Reset(wait)
		}
		return
	}
	i := max(0, slices.IndexFunc(c.queue, func(t *cardTurn) bool { return t.first }))
	t := c.queue[i]
	c.queue = slices.Delete(c.queue, i, i+1)
	c.busy = true
	close(t.ready)
}

// Announce shows text on the server's card, to one player (a SteamID64) or
// to everyone (to ""), in its turn, and returns the server's reply. first is
// for the manager's own announcements, which go before plugins' cards.
func (in *Instance) Announce(ctx context.Context, to, text, by string, first bool) (string, error) {
	if err := in.cards.take(ctx, first); err != nil {
		return "", fmt.Errorf("%w: %w", ErrCardWait, err)
	}
	line := "announce " + text
	if to != "" {
		line = "announce-to " + to + " " + text
	}
	reply, err := in.Command(ctx, line, by)
	in.cards.done()
	return reply, err
}

// sawCard notes an [announcement] line: "[announcement -> Bob] text".
func (in *Instance) sawCard(line string) {
	_, text, _ := strings.Cut(line, "] ")
	in.cards.saw(text)
}
