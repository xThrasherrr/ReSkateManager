package plugins

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/xThrasherrr/ReSkateManager/internal/alerts"
)

const hook = "https://discord.com/api/webhooks/123/abc-DEF_9"

// discord.post takes only a Discord webhook and a card, and needs its grant;
// a refusal says what to change.
func TestDiscordArguments(t *testing.T) {
	m := newManager(t)
	m.Discord = func(context.Context, string, alerts.Alert) error { return nil }
	addPlugin(t, m, "poster", "api = 0\npermissions = [\"discord.post\"]", `
local H = "`+hook+`"
local calls = {
  function() return discord.post(nil, "hi") end,
  function() return discord.post("https://example.com/api/webhooks/1/x", "hi") end,
  function() return discord.post(H, 5) end,
  function() return discord.post(H, {title = " ", lines = {"\n"}}) end,
  function() return discord.post(H, "hi") end,
}
for i, f in ipairs(calls) do
  log(i, select(2, pcall(f)))
end
`)
	enable(t, m, "poster")
	checkLines(t, m, "poster", []string{
		`bad argument #1 to 'discord.post' (webhook address expected, got nil)`,
		`bad argument #1 to 'discord.post' (not a Discord webhook address`,
		`bad argument #2 to 'discord.post' (string or card expected, got number)`,
		`bad argument #2 to 'discord.post' (no text to show)`,
		`discord.post needs discord.post (Post to Discord webhooks), which this plugin wasn't granted: grant it on the plugin's card`,
	})
}

// A post goes to the webhook named, its text escaped, in order; a failure
// reaches the callback without the webhook's address.
func TestDiscordPost(t *testing.T) {
	m := newManager(t)
	var mu sync.Mutex
	var got []alerts.Alert
	m.Discord = func(_ context.Context, webhook string, a alerts.Alert) error {
		mu.Lock()
		defer mu.Unlock()
		if webhook != hook {
			t.Errorf("webhook %q", webhook)
		}
		if a.Title == "fail" {
			return errors.New("discord: 404 Unknown Webhook")
		}
		got = append(got, a)
		return nil
	}
	addPlugin(t, m, "poster", "api = 0\npermissions = [\"discord.post\"]", `
local H = "`+hook+`"
for _, card in ipairs({
  {title = "Report #1", lines = {"By: **Zee** @everyone", "", "Reason: [link](https://x.io)"}},
  "just text",
  {title = "fail"},
}) do
  discord.post(H, card, function(ok, reply) log(ok, reply) end)
end
`)
	grant(t, m, "poster", DiscordPost)
	enable(t, m, "poster")
	waitFor(t, func() bool { return len(lines(m, "poster")) == 3 })
	wantLines := []string{"true Posted.", "true Posted.", "false discord: 404 Unknown Webhook"}
	for i, l := range lines(m, "poster") {
		if l != wantLines[i] {
			t.Errorf("line %d: %q, want %q", i+1, l, wantLines[i])
		}
	}
	mu.Lock()
	defer mu.Unlock()
	want := []alerts.Alert{
		{Kind: alerts.Plugin, Title: "Report #1", Text: "By: \\*\\*Zee\\*\\* @everyone\nReason: \\[link](https://x.io)"},
		{Kind: alerts.Plugin, Text: "just text"},
	}
	if len(got) != len(want) {
		t.Fatalf("posted %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("post %d: %+v, want %+v", i+1, got[i], want[i])
		}
	}
}
