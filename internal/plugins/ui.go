package plugins

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/auth"
	"github.com/xThrasherrr/ReSkateManager/internal/instance"
	"github.com/xThrasherrr/ReSkateManager/internal/plugins/luavm"
	"github.com/xThrasherrr/ReSkateManager/internal/serverconfig"
	"github.com/xThrasherrr/ReSkateManager/internal/supervisor"
)

// A server shows one card at a time (instance.Announce), so a card waits for
// its turn on a goroutine of its own, without holding up the plugin's other
// commands. One that waits too long is dropped: a welcome a minute late
// greets nobody.
var cardWait = time.Minute // tests make it shorter

const maxCards = 16 // cards a plugin may have waiting

// uiCall answers ui.card(server, steamId or "all", card [, fn]). Today a
// card is one line on the server's announcement card, its title and lines
// joined with " · "; a server with cards of its own (ReSkate change B) will
// take the same call.
func (r *runner) uiCall(name string, args []any) ([]any, error) {
	if name != "ui.card" {
		return nil, fmt.Errorf("no function %s", name)
	}
	var a action
	if n := len(args); n > 3 {
		if fn, ok := args[n-1].(luavm.Ref); ok {
			a.fn, a.hasFn = fn, true
			args = args[:n-1]
		}
	}
	arg := func(i int) any {
		if i < len(args) {
			return args[i]
		}
		return nil
	}
	in, err := r.server(name, 1, arg(0))
	if err != nil {
		return nil, err
	}
	to, ok := arg(1).(string)
	if !ok || to != "all" && !serverconfig.SteamIDRe.MatchString(to) {
		return nil, badArg(2, name, `SteamID64 or "all"`, arg(1))
	}
	text, err := cardText(name, arg(2))
	if err != nil {
		return nil, err
	}
	if err := r.need(name, auth.PlayersChat, in.ID()); err != nil {
		return nil, err
	}
	if r.cardsWaiting.Load() >= maxCards {
		r.p.mu.Lock()
		r.p.stats.Unsent++
		r.p.mu.Unlock()
		return []any{false, fmt.Sprintf("%d cards are waiting for their turn already", maxCards)}, nil
	}
	a.gen, a.server, a.in, a.call = r.gen, in.ID(), in, name
	a.audit, a.detail = "player.say", "card: "+text
	if to == "all" {
		to = ""
	} else {
		a.audit, a.detail = "player.msg", to+" card: "+text
	}
	if a.hasFn {
		r.vm.Keep(a.fn)
	}
	ctx := r.cardCtx
	r.cardsWaiting.Add(1)
	r.cards.Go(func() {
		ctx, cancel := context.WithTimeout(ctx, cardWait)
		reply, err := in.Announce(ctx, to, text, "plugin:"+r.p.ID, false)
		cancel()
		r.cardsWaiting.Add(-1)
		if errors.Is(err, instance.ErrCardWait) {
			r.p.mu.Lock()
			r.p.stats.Unsent++
			r.p.mu.Unlock()
			r.hand(a, "", fmt.Errorf("it waited %s for its turn at the server's card", cardWait))
			return
		}
		r.finish(a, reply, err)
	})
	return []any{true}, nil
}

// cardText is a card as text: a string, or {title =, lines = {...}}, its
// parts joined with " · " and cut where the server cuts chat.
func cardText(call string, v any) (string, error) {
	var parts []string
	if s, ok := v.(string); ok {
		parts = append(parts, s)
	} else {
		if _, ok := v.(*luavm.Table); !ok {
			return "", badArg(3, call, "string or card", v)
		}
		opts, err := options(call, 3, v, "title", "lines")
		if err != nil {
			return "", err
		}
		if t, ok := opts["title"]; ok {
			s, ok := t.(string)
			if !ok {
				return "", fmt.Errorf("bad argument #3 to '%s' (title is a string)", call)
			}
			parts = append(parts, s)
		}
		if l, ok := opts["lines"]; ok {
			lines, ok := l.(*luavm.Table)
			if !ok {
				return "", fmt.Errorf("bad argument #3 to '%s' (lines is a list of strings)", call)
			}
			for _, line := range lines.List() {
				s, ok := line.(string)
				if !ok {
					return "", fmt.Errorf("bad argument #3 to '%s' (lines is a list of strings)", call)
				}
				parts = append(parts, s)
			}
		}
	}
	var kept []string
	for _, p := range parts {
		if p = strings.TrimSpace(supervisor.CleanText(p)); p != "" {
			kept = append(kept, p)
		}
	}
	if len(kept) == 0 {
		return "", fmt.Errorf("bad argument #3 to '%s' (no text to show)", call)
	}
	return chat(call, 3, strings.Join(kept, " · "))
}
