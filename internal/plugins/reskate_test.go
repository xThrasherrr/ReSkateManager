package plugins

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/auth"
	"github.com/xThrasherrr/ReSkateManager/internal/logparse"
)

// reskate.send takes what JSON holds, as deep and as long as the bridge and
// a console line do, and the ReSkate plugin gets it as it was sent, out of
// the console's view and the audit log.
func TestReSkateSend(t *testing.T) {
	m := newManager(t)
	reg := servers(t, m, "s1")
	in := fakeServer(t, reg, "s1")
	addPlugin(t, m, "talks", "api = 0\npermissions = [\"reskate.send\"]", `
local S = "s1"
local function deep(n) local t = {} for _ = 2, n do t = {t} end return t end
local function said(what) return function(ok, reply) log(what, ok, reply) end end
local calls = {
  function() return reskate.send("nope", "races", 1) end,
  function() return reskate.send(S, "two words", 1) end,
  function() return reskate.send(S, "races") end,
  function() return reskate.send(S, "races", print) end,
  function() return reskate.send(S, "races", {1, 2, x = 3}) end,
  function() return reskate.send(S, "races", {[2] = "b"}) end,
  function() return reskate.send(S, "races", 0/0) end,
  function() return reskate.send(S, "races", deep(9)) end,
  function() return reskate.send(S, "races", ("x"):rep(3062)) end,
}
for i, f in ipairs(calls) do
  log(i, select(2, pcall(f)))
end
reskate.send(S, "races", {won = "Bea", laps = {1.5, 2}, n = 3, ok = true, none = {}}, said("table"))
reskate.send(S, "races", "hi", said("text"))
reskate.send(S, "races", deep(8), said("deep"))
reskate.send(S, "races", ("x"):rep(3061), said("long"))
`)
	grant(t, m, "talks", ReSkateSend)
	enable(t, m, "talks")
	want := []string{
		`bad argument #1 to 'reskate.send' (no server "nope")`,
		`bad argument #2 to 'reskate.send' (ReSkate plugin's id expected, got string)`,
		`bad argument #3 to 'reskate.send' (nothing to send)`,
		`bad argument #3 to 'reskate.send' (can't send a function)`,
		`bad argument #3 to 'reskate.send' (a table is a list, 1 to n, or has string keys)`,
		`bad argument #3 to 'reskate.send' (a table is a list, 1 to n, or has string keys)`,
		`bad argument #3 to 'reskate.send' (JSON has no NaN)`,
		`bad argument #3 to 'reskate.send' (tables go 8 deep at most)`,
		// "rsm races " and base64 of 3063 bytes come to 4094 of the 4096.
		`bad argument #3 to 'reskate.send' (3064 bytes as JSON, and a message to races holds 3063)`,
	}
	waitFor(t, func() bool { return hasLine(m, "talks", "long ") })
	got := lines(m, "talks")
	for i, w := range want {
		if i >= len(got) || !strings.HasPrefix(got[i], fmt.Sprint(i+1)+" ") || !strings.Contains(got[i], w) {
			t.Errorf("call %d: want %q in %q", i+1, w, got)
		}
	}
	for _, w := range []string{"table", "text", "deep", "long"} {
		if !hasLine(m, "talks", w+" true Ran by a plugin.") {
			t.Errorf("%s: %q", w, got)
		}
	}

	ctx := context.Background()
	reply, err := in.Command(ctx, "told races", "test")
	wantTold := strings.Join([]string{"told",
		`{"laps":[1.5,2],"n":3,"none":{},"ok":true,"won":"Bea"}`,
		`"hi"`,
		strings.Repeat("[", 7) + "{}" + strings.Repeat("]", 7),
		`"` + strings.Repeat("x", 3061) + `"`,
	}, " | ")
	if err != nil || reply != wantTold {
		t.Errorf("the ReSkate plugin got %q, %v; want %q", reply, err, wantTold)
	}
	for _, e := range in.Console() {
		if e.Kind == logparse.KindInput && strings.HasPrefix(e.Text, "rsm ") || strings.Contains(e.Text, "Ran by a plugin.") {
			t.Errorf("in the console: %q", e.Text)
		}
	}
	entries, err := m.Store.AuditLog(ctx, "", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Username == "plugin:talks" {
			t.Errorf("audited %+v", e)
		}
	}
	if s, _ := m.Get("talks"); s.Stats.Sent != 4 {
		t.Errorf("stats %+v", s.Stats)
	}
}

// A plugin's messages don't wait behind its server.* commands: with a
// hundred says waiting, a message still goes at once.
func TestReSkateSendOwnQueue(t *testing.T) {
	m := newManager(t)
	reg := servers(t, m, "s1")
	fakeServer(t, reg, "s1")
	addPlugin(t, m, "loud", "api = 0\npermissions = [\"players.chat\", \"reskate.send\"]", `
every(0.1, function()
  for i = 1, 100 do server.say("s1", "spam " .. i) end
end)
after(0.5, function()
  reskate.send("s1", "races", {n = 1}, function(ok, reply) log("told", ok, reply) end)
end)
`)
	grant(t, m, "loud", auth.PlayersChat, ReSkateSend)
	start := time.Now()
	enable(t, m, "loud")
	waitFor(t, func() bool { return hasLine(m, "loud", "told ") })
	if d := time.Since(start); d > 3*time.Second || !hasLine(m, "loud", "told true Ran by a plugin.") {
		t.Errorf("after %s: %q", d, lines(m, "loud"))
	}
	if s, _ := m.Get("loud"); s.Stats.Unsent == 0 {
		t.Errorf("the says never filled their queue: %+v", s.Stats)
	}
}
