package plugins

import (
	"context"
	"errors"
	"fmt"
	"slices"
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

// cardText is a card as text: its title and lines joined with " · " and cut
// where the server cuts chat.
func cardText(call string, v any) (string, error) {
	title, lines, err := cardParts(call, 3, v)
	if err != nil {
		return "", err
	}
	if title != "" {
		lines = append([]string{title}, lines...)
	}
	return chat(call, 3, strings.Join(lines, " · "))
}

// cardParts reads a card, argument n: a string, which is one line, or
// {title =, lines = {...}}. Each part is one line, and empty ones are dropped.
func cardParts(call string, n int, v any) (title string, lines []string, err error) {
	clean := func(s string) string { return strings.TrimSpace(supervisor.CleanText(s)) }
	if s, ok := v.(string); ok {
		lines = []string{clean(s)}
	} else {
		if _, ok := v.(*luavm.Table); !ok {
			return "", nil, badArg(n, call, "string or card", v)
		}
		opts, err := options(call, n, v, "title", "lines")
		if err != nil {
			return "", nil, err
		}
		if t, ok := opts["title"]; ok {
			s, ok := t.(string)
			if !ok {
				return "", nil, fmt.Errorf("bad argument #%d to '%s' (title is a string)", n, call)
			}
			title = clean(s)
		}
		if l, ok := opts["lines"]; ok {
			list, ok := l.(*luavm.Table)
			if !ok {
				return "", nil, fmt.Errorf("bad argument #%d to '%s' (lines is a list of strings)", n, call)
			}
			for _, line := range list.List() {
				s, ok := line.(string)
				if !ok {
					return "", nil, fmt.Errorf("bad argument #%d to '%s' (lines is a list of strings)", n, call)
				}
				lines = append(lines, clean(s))
			}
		}
	}
	lines = slices.DeleteFunc(lines, func(s string) bool { return s == "" })
	if title == "" && len(lines) == 0 {
		return "", nil, fmt.Errorf("bad argument #%d to '%s' (no text to show)", n, call)
	}
	return title, lines, nil
}
