package plugins

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/alerts"
	"github.com/xThrasherrr/ReSkateManager/internal/discord"
	"github.com/xThrasherrr/ReSkateManager/internal/plugins/luavm"
)

// A plugin's posts go out one at a time, in order, on a goroutine of their
// own, so Discord's waits on its rate hold up nothing else of the plugin's.
const maxPosts = 16 // posts a plugin may have waiting

// postTime is the longest a post may take, Discord's waits included.
var postTime = 2 * time.Minute

// discordCall answers discord.post(webhook, card [, fn]): card is text, or
// {title =, lines = {...}}, pinging nobody. Its lines show as written; its
// title isn't escaped, since a backslash may show there.
func (r *runner) discordCall(name string, args []any) ([]any, error) {
	if name != "discord.post" {
		return nil, fmt.Errorf("no function %s", name)
	}
	var a action
	if n := len(args); n > 2 {
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
	webhook, ok := arg(0).(string)
	if !ok {
		return nil, badArg(1, name, "webhook address", arg(0))
	}
	if webhook == "" || alerts.CheckWebhook(webhook) != nil {
		return nil, fmt.Errorf("bad argument #1 to '%s' (not a Discord webhook address: copy one from the channel's Integrations, Webhooks)", name)
	}
	title, lines, err := cardParts(name, 2, arg(1))
	if err != nil {
		return nil, err
	}
	if err := r.need(name, DiscordPost, ""); err != nil {
		return nil, err
	}
	if r.m.Discord == nil {
		return []any{false, "this manager can't post to Discord"}, nil
	}
	a.gen, a.call, a.audit, a.detail = r.gen, name, "plugin.discord", title
	if title == "" {
		a.detail = lines[0]
	}
	for i, l := range lines {
		lines[i] = discord.Escape(l)
	}
	p := post{action: a, webhook: webhook, alert: alerts.Alert{Kind: alerts.Plugin, Title: title, Text: strings.Join(lines, "\n")}}
	r.postOnce.Do(func() { r.posting.Go(r.postLoop) })
	select {
	case r.posts <- p:
	default:
		r.p.mu.Lock()
		r.p.stats.Unsent++
		r.p.mu.Unlock()
		return []any{false, fmt.Sprintf("%d posts are waiting already", maxPosts)}, nil
	}
	if a.hasFn {
		r.vm.Keep(a.fn)
	}
	return []any{true}, nil
}

// post is a discord.post waiting its turn.
type post struct {
	action
	webhook string
	alert   alerts.Alert
}

// postLoop sends the plugin's posts until it stops; the first post starts it.
func (r *runner) postLoop() {
	for {
		var p post
		select {
		case <-r.quit:
			return
		case p = <-r.posts:
		}
		ctx, cancel := context.WithTimeout(r.ctx, postTime)
		err := r.m.Discord(ctx, p.webhook, p.alert)
		cancel()
		reply := "Posted."
		if err != nil {
			reply = ""
		}
		if !r.finish(p.action, reply, err) {
			return
		}
	}
}
