package plugins

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/instance"
)

// cardTime makes a second of a card's time d and its longest wait w, for
// one test. Call it before newManager.
func cardTime(t *testing.T, d, w time.Duration) {
	unit, wait := instance.CardUnit, cardWait
	t.Cleanup(func() { instance.CardUnit, cardWait = unit, wait })
	instance.CardUnit, cardWait = d, w
}

// ui.card names a player or "all", and takes text or {title, lines}; a
// refusal says what to change.
func TestCardArguments(t *testing.T) {
	m := newManager(t)
	servers(t, m, "s1")
	addPlugin(t, m, "cards", "api = 0\npermissions = [\"players.chat\"]", `
local S, P = "s1", "`+zee+`"
local calls = {
  function() return ui.card(S, "zee", "hi") end,
  function() return ui.card(S, nil, "hi") end,
  function() return ui.card(S, P, 5) end,
  function() return ui.card(S, P, {title = 1}) end,
  function() return ui.card(S, P, {lines = "hi"}) end,
  function() return ui.card(S, P, {lines = {"a", 2}}) end,
  function() return ui.card(S, P, {colour = "red"}) end,
  function() return ui.card(S, P, {title = " ", lines = {"\n"}}) end,
}
for i, f in ipairs(calls) do
  log(i, select(2, pcall(f)))
end
`)
	grant(t, m, "cards", "players.chat")
	enable(t, m, "cards")
	checkLines(t, m, "cards", []string{
		`bad argument #2 to 'ui.card' (SteamID64 or "all" expected, got string)`,
		`bad argument #2 to 'ui.card' (SteamID64 or "all" expected, got nil)`,
		`bad argument #3 to 'ui.card' (string or card expected, got number)`,
		`bad argument #3 to 'ui.card' (title is a string)`,
		`bad argument #3 to 'ui.card' (lines is a list of strings)`,
		`bad argument #3 to 'ui.card' (lines is a list of strings)`,
		`bad argument #3 to 'ui.card' (no option colour; it takes title, lines)`,
		`bad argument #3 to 'ui.card' (no text to show)`,
	})
}

// Two players joining together each get their card whole, and an
// announcement of the manager's goes up before a card still waiting. A flood
// is refused past maxCards, and what waits too long says so; turning the
// plugin off drops what still waits.
func TestCards(t *testing.T) {
	cardTime(t, 40*time.Millisecond, 2*time.Second)
	callTime(t, 5*time.Second)
	m := newManager(t)
	reg := servers(t, m, "s1")
	in := fakeServer(t, reg, "s1")
	addPlugin(t, m, "welcome", "api = 0\npermissions = [\"players.chat\"]", `
on("player.joined", function(e)
  ui.card(e.server, e.id, {title = "Welcome back", lines = {e.name, "#1 on playtime"}}, function(ok, reply)
    log("card", e.name, ok, reply)
  end)
end)
on("chat", function(e)
  if e.text == "flood" then
    for i = 1, 20 do
      local ok, why = ui.card(e.server, "all", "flood " .. i, function(ok, reply) log("flood", i, ok, reply) end)
      if not ok then
        log("refused", i, why)
        return
      end
    end
  end
end)
`)
	grant(t, m, "welcome", "players.chat")
	enable(t, m, "welcome")
	ctx := context.Background()
	cards := func() []string {
		var out []string
		for _, e := range in.Console() {
			if e.Tag == "announcement" {
				out = append(out, e.Text)
			}
		}
		return out
	}
	for _, c := range []string{"join " + zee + " Zee", "join 76561198000000002 Bea"} {
		if _, err := in.Command(ctx, c, "test"); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, func() bool { return len(cards()) == 1 })
	if _, err := in.Announce(ctx, "", "Server restart in 5 minutes", "announcements", true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return hasLine(m, "welcome", "card Zee") && hasLine(m, "welcome", "card Bea") })
	got := cards()
	welcome := []string{"[announcement -> Zee] Welcome back · Zee · #1 on playtime", "[announcement -> Bea] Welcome back · Bea · #1 on playtime"}
	if len(got) != 3 || got[1] != "[announcement] Server restart in 5 minutes" || !slices.Contains(welcome, got[0]) || !slices.Contains(welcome, got[2]) || got[0] == got[2] {
		t.Fatalf("cards %q", got)
	}
	for _, w := range []string{"card Zee true Announced to Zee.", "card Bea true Announced to Bea."} {
		if !hasLine(m, "welcome", w) {
			t.Errorf("no %q in %q", w, lines(m, "welcome"))
		}
	}

	notice := m.Notice("s1")
	chat := func(text string) {
		notice(instance.Notice{Kind: instance.NoticeChat, Player: instance.Player{Name: "Zee", ID: zee}, Text: text})
	}
	chat("flood")
	waitFor(t, func() bool {
		n := 0
		for _, l := range lines(m, "welcome") {
			if strings.HasPrefix(l, "flood ") {
				n++
			}
		}
		return n == maxCards
	})
	if !hasLine(m, "welcome", "refused 17 16 cards are waiting for their turn already") {
		t.Errorf("no refusal in %q", lines(m, "welcome"))
	}
	shown, late := 0, 0
	for _, l := range lines(m, "welcome") {
		switch {
		case strings.HasPrefix(l, "flood ") && strings.HasSuffix(l, " true Announced."):
			shown++
		case strings.HasPrefix(l, "flood ") && strings.HasSuffix(l, " false it waited 2s for its turn at the server's card"):
			late++
		case strings.HasPrefix(l, "flood "):
			t.Errorf("flood: %q", l)
		}
	}
	s, _ := m.Get("welcome")
	if shown == 0 || late == 0 || s.Stats.Unsent != uint64(1+late) {
		t.Errorf("%d shown, %d late, %d unsent", shown, late, s.Stats.Unsent)
	}

	chat("flood")
	waitFor(t, func() bool { return hasLine(m, "welcome", "refused") && len(cards()) > 3+shown })
	start := time.Now()
	if err := m.SetEnabled(ctx, "welcome", false); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("turning it off took %s, waiting on its cards", d)
	}
}
