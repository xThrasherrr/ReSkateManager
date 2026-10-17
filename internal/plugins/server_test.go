package plugins

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/auth"
	"github.com/xThrasherrr/ReSkateManager/internal/instance"
	"github.com/xThrasherrr/ReSkateManager/internal/logparse"
)

const zee = "76561198000000001"

// servers gives m a registry of servers with these IDs, none of them running.
func servers(t *testing.T, m *Manager, ids ...string) *instance.Registry {
	t.Helper()
	reg := instance.NewRegistry(slog.New(slog.NewTextHandler(io.Discard, nil)))
	reg.Wire = func(in *instance.Instance) { in.OnNotice = m.Notice(in.ID()) }
	for _, id := range ids {
		reg.Add(instance.Def{ID: id, Name: strings.ToUpper(id), Dir: t.TempDir()})
	}
	m.Servers = reg
	return reg
}

// fakeServer starts the fake ReSkate server as id in reg, and stops it after.
func fakeServer(t *testing.T, reg *instance.Registry, id string) *instance.Instance {
	t.Helper()
	in, _ := reg.Get(id)
	if out, err := exec.Command("go", "build", "-o", in.Def().Exe(), "../instance/testdata/fakeserver").CombinedOutput(); err != nil {
		t.Fatalf("build fake server: %v: %s", err, out)
	}
	if err := in.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		in.Stop(ctx)
	})
	waitFor(t, func() bool { return in.State() == instance.Running })
	return in
}

func grant(t *testing.T, m *Manager, id string, keys ...string) {
	t.Helper()
	if err := m.SetGranted(context.Background(), id, keys); err != nil {
		t.Fatal(err)
	}
}

func allGrants() []string {
	var keys []string
	for _, g := range Grants {
		keys = append(keys, g.Key)
	}
	return keys
}

// callPerms is what each server.* call needs, as the routes' table in
// internal/api is for the panel's: "" for nothing, permissions joined by
// "+" when lacking any one turns the call away. A call refused for the
// wrong permission fails the test as surely as one let through.
var callPerms = []struct{ lua, perms string }{
	{`server.list()`, ""},
	{`server.info(S)`, ""},
	{`server.players(S)`, ""},
	{`server.say(S, "hi")`, "players.chat"},
	{`server.msg(S, P, "hi")`, "players.chat"},
	{`server.kick(S, P)`, "players.kick"},
	{`server.kick(S, P, 30)`, "players.kick"},
	{`server.ban(S, P)`, "players.ban"},
	{`server.unban(S, P)`, "players.ban"},
	{`server.role(S, P, "vip")`, "ingame.admins.manage"},
	{`server.role(S, P, "none")`, "ingame.admins.manage"},
	{`server.teleport(S, {P}, {x = 1, y = 2, z = 3})`, "console.exec"},
	{`server.teleport(S, P, P)`, "console.exec"},
	{`server.command(S, "status")`, "console.exec"},
	{`server.command(S, "players")`, "console.exec"},
	{`server.command(S, "say hi")`, "console.exec+players.chat"},
	{`server.command(S, "MSG " .. P .. " hi")`, "console.exec+players.chat"},
	{`server.command(S, "kick " .. P)`, "console.exec+players.kick"},
	{`server.command(S, "vote kick " .. P)`, "console.exec+players.kick"},
	{`server.command(S, "ban " .. P)`, "console.exec+players.ban"},
	{`server.command(S, "unban " .. P)`, "console.exec+players.ban"},
	{`server.command(S, "role " .. P .. " admin")`, "console.exec+ingame.admins.manage"},
	{`server.command(S, "roles reload")`, "console.exec+ingame.admins.manage"},
	{`server.command(S, "noclip on")`, "console.exec+settings.edit"},
	{`server.command(S, "password hunter2")`, "console.exec+settings.edit"},
	{`server.command(S, "maps reload")`, "console.exec+settings.edit"},
	{`server.command(S, "frobnicate")`, "console.exec+settings.edit"}, // unknown: maybe a newer server's setting
	{`ui.card(S, P, "hi")`, "players.chat"},
	{`ui.card(S, "all", {title = "Hi", lines = {"there"}})`, "players.chat"},
}

// Every call is refused without each permission it needs, and only then:
// for each grant a plugin can have, every call is run with all the others.
func TestCallPermissions(t *testing.T) {
	m := newManager(t)
	servers(t, m, "s1")
	var main strings.Builder
	main.WriteString("local S, P = \"s1\", \"" + zee + "\"\nlocal calls = {\n")
	for _, c := range callPerms {
		main.WriteString("  function() return " + c.lua + " end,\n")
	}
	main.WriteString(`}
-- Each command gets a function to call, so one that fails on the stopped
-- server isn't logged among the results.
local quiet = function() end
for t, names in pairs({[server] = {"say", "msg", "kick", "ban", "unban", "teleport", "role", "command"}, [ui] = {"card"}}) do
  for _, name in ipairs(names) do
    local f = t[name]
    t[name] = function(...)
      local args = table.pack(...)
      args[args.n + 1] = quiet
      return f(table.unpack(args, 1, args.n + 1))
    end
  end
end
for i, f in ipairs(calls) do
  local ok, err = pcall(f)
  log(i, ok and "ran" or err)
end
`)
	toml := "api = 0\npermissions = [\"" + strings.Join(allGrants(), "\", \"") + "\"]"
	addPlugin(t, m, "acts", toml, main.String())
	enable(t, m, "acts")
	run := func(granted []string) map[int]string {
		t.Helper()
		grant(t, m, "acts", granted...)
		if err := m.Reload("acts"); err != nil {
			t.Fatal(err)
		}
		waitFor(t, func() bool { s, _ := m.Get("acts"); return s.State != Starting })
		got := map[int]string{}
		re := regexp.MustCompile(`^(\d+) (.*)$`)
		for _, l := range lines(m, "acts") {
			var i int
			if mm := re.FindStringSubmatch(l); mm != nil {
				fmt.Sscan(mm[1], &i)
				got[i] = mm[2]
			}
		}
		if len(got) != len(callPerms) {
			t.Fatalf("granted %v: %d results of %d: %q", granted, len(got), len(callPerms), lines(m, "acts"))
		}
		return got
	}
	check := func(granted []string) {
		t.Helper()
		got := run(granted)
		for i, c := range callPerms {
			res := got[i+1]
			var missing []string
			for _, p := range strings.Split(c.perms, "+") {
				if p != "" && !slices.Contains(granted, p) {
					missing = append(missing, p)
				}
			}
			switch {
			case len(missing) == 0 && res != "ran":
				t.Errorf("granted %v: %s was refused: %s", granted, c.lua, res)
			case len(missing) > 0 && (res == "ran" || !strings.Contains(res, "needs "+missing[0])):
				t.Errorf("granted %v: %s: %s, want it refused for %s", granted, c.lua, res, missing[0])
			}
		}
	}
	check(nil)
	check(allGrants())
	for _, g := range allGrants() {
		check(slices.DeleteFunc(allGrants(), func(k string) bool { return k == g }))
	}
}

// The table covers every server.* and ui.* call the prelude makes, and every
// grant.
func TestCallPermissionsCoverEveryCall(t *testing.T) {
	covered, granted := map[string]bool{}, map[string]bool{}
	for _, c := range callPerms {
		covered[regexp.MustCompile(`^(\w+\.\w+)\(`).FindStringSubmatch(c.lua)[1]] = true
		for _, p := range strings.Split(c.perms, "+") {
			granted[p] = true
		}
	}
	for _, table := range []string{"server", "ui"} {
		m := regexp.MustCompile(table + ` = api\("` + table + `", \{([^}]*)\}\)`).FindStringSubmatch(prelude)
		if m == nil {
			t.Fatalf("no %s.* list in the prelude", table)
		}
		for _, q := range strings.Split(m[1], ",") {
			if name := table + "." + strings.Trim(strings.TrimSpace(q), `"`); !covered[name] {
				t.Errorf("%s isn't in callPerms", name)
			}
		}
	}
	for _, g := range Grants {
		if !granted[g.Key] {
			t.Errorf("no call in callPerms needs %s", g.Key)
		}
		for _, c := range g.Calls {
			if !covered[c] {
				t.Errorf("%s opens %s, which isn't in callPerms", g.Key, c)
			}
		}
	}
}

// A call names one of the plugin's servers, with what it acts on in the
// shapes the server takes; a refusal says what to change.
func TestCallArguments(t *testing.T) {
	m := newManager(t)
	reg := servers(t, m, "s1", "s2")
	// s1 ran a ReSkate from before 2.0.8, whose kicks have no time.
	s1, _ := reg.Get("s1")
	if err := os.WriteFile(s1.Def().ConfigPath(), []byte(`{"players": {"afk_kick_minutes": 0}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	addPlugin(t, m, "picky", "api = 0\nservers = [\"s1\"]\npermissions = [\"players.chat\", \"players.kick\", \"console.exec\", \"ingame.admins.manage\"]", `
local S, P = "s1", "`+zee+`"
local calls = {
  function() return server.say("s2", "hi") end,
  function() return server.say("nope", "hi") end,
  function() return server.say(nil, "hi") end,
  function() return server.say(S, "  \n ") end,
  function() return server.say(S, 42) end,
  function() return server.msg(S, "zee", "hi") end,
  function() return server.msg(S, 76561198000000001, "hi") end,
  function() return server.kick(S, P, 1441) end,
  function() return server.kick(S, P, 1.5) end,
  function() return server.kick(S, P, 5) end,
  function() return server.role(S, P, "Bad Role!") end,
  function() return server.teleport(S, {}, P) end,
  function() return server.teleport(S, {P, "x"}, P) end,
  function() return server.teleport(S, P, {x = 1, y = 2}) end,
  function() return server.teleport(S, P, {x = 1, y = 2, z = 1e6}) end,
  function() return server.command(S, "") end,
  function() return server.command(S, "status\nquit") end,
  function() return server.command(S, ("x"):rep(1025)) end,
  function() return server.command(S, "poll-run quit | Stop? | yes | no") end,
  function() return server.ban(S, P) end,
  function() return #server.list(), server.list()[1].id, server.info(S).name, server.info(S).state, #server.players(S) end,
}
for i, f in ipairs(calls) do
  log(i, select(2, pcall(f)))
end
`)
	grant(t, m, "picky", "players.chat", "players.kick", "console.exec", "ingame.admins.manage")
	enable(t, m, "picky")
	want := []string{
		`this plugin doesn't run on "s2"`,
		`this plugin doesn't run on "nope"`,
		`bad argument #1 to 'server.say' (server ID expected, got nil)`,
		`bad argument #2 to 'server.say' (no text to send)`,
		`bad argument #2 to 'server.say' (string expected, got number)`,
		`bad argument #2 to 'server.msg' (SteamID64 expected, got string)`,
		`bad argument #2 to 'server.msg' (SteamID64 expected, got number)`,
		`bad argument #3 to 'server.kick' (minutes from 1 to 1440`,
		`bad argument #3 to 'server.kick' (minutes from 1 to 1440`,
		`server.kick can't give a kick on s1 a time: its kicks last until it restarts, and ReSkate 2.0.8 gives them one`,
		`bad argument #3 to 'server.role' (a role's name, or "none")`,
		`bad argument #2 to 'server.teleport' (nobody to teleport)`,
		`bad argument #2 to 'server.teleport' (a list of SteamID64s)`,
		`bad argument #3 to 'server.teleport' (a place is {x =, y =, z =}`,
		`bad argument #3 to 'server.teleport' (a place is {x =, y =, z =}`,
		`bad argument #2 to 'server.command' (an empty command)`,
		`bad argument #2 to 'server.command' (a command must be one line)`,
		`bad argument #2 to 'server.command' (a command can be at most 1024 bytes)`,
		`server.command can't run poll-run: only owners can`,
		`server.ban needs players.ban (Ban and unban players), which this plugin wasn't granted: add "players.ban" to permissions in its plugin.toml and reload it, then grant it on its card`,
		`1 s1 S1 stopped 0`,
	}
	got := lines(m, "picky")
	if len(got) != len(want) {
		t.Fatalf("%d lines, want %d: %q", len(got), len(want), got)
	}
	for i, w := range want {
		if !strings.HasPrefix(got[i], fmt.Sprint(i+1)+" ") || !strings.Contains(got[i], w) {
			t.Errorf("call %d: %q, want %q", i+1, got[i], w)
		}
	}
}

// Grants are only what plugin.toml asks for, and only the panel's
// permissions that open a call; what it no longer asks for counts for
// nothing, and what's granted outlives a restart.
func TestGrants(t *testing.T) {
	m := newManager(t)
	addPlugin(t, m, "p", "api = 0\npermissions = [\"players.chat\", \"players.ban\", \"audit.view\"]", "log(1)")
	for keys, want := range map[string]string{
		"audit.view":   "plugins can't be given audit.view",
		"players.kick": "its plugin.toml doesn't ask for players.kick",
		"made.up":      "plugins can't be given made.up",
	} {
		if err := m.SetGranted(context.Background(), "p", []string{keys}); !errors.Is(err, ErrBadGrant) || !strings.Contains(err.Error(), want) {
			t.Errorf("grant %s: %v, want %q", keys, err, want)
		}
	}
	if err := m.SetGranted(context.Background(), "nope", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("no such plugin: %v", err)
	}
	grant(t, m, "p", "players.ban", "players.chat", "players.chat")
	if s, _ := m.Get("p"); !slices.Equal(s.Granted, []string{"players.ban", "players.chat"}) {
		t.Fatalf("granted %q", s.Granted)
	}
	addPlugin(t, m, "p", "api = 0\npermissions = [\"players.chat\"]", "log(1)")
	if s, _ := m.Get("p"); !slices.Equal(s.Granted, []string{"players.chat"}) {
		t.Errorf("no longer asked for: granted %q", s.Granted)
	}
	again := &Manager{Dir: m.Dir, Store: m.Store, Log: m.Log, LogPath: m.LogPath}
	if err := again.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if s, _ := again.Get("p"); !slices.Equal(s.Granted, []string{"players.chat"}) {
		t.Errorf("after a restart: granted %q", s.Granted)
	}
	grant(t, m, "p")
	if s, _ := m.Get("p"); len(s.Granted) != 0 {
		t.Errorf("taken back: granted %q", s.Granted)
	}
}

// What a plugin sends reaches the server as the panel's commands do: in the
// console as the plugin's, in the audit log, and back to a callback with
// the reply.
func TestActions(t *testing.T) {
	m := newManager(t)
	reg := servers(t, m, "s1")
	in := fakeServer(t, reg, "s1")
	addPlugin(t, m, "acts", "api = 0\npermissions = [\"players.chat\", \"players.kick\", \"players.ban\", \"console.exec\", \"settings.edit\"]", `
local function said(what)
  return function(ok, reply) log(what, ok, reply) end
end
on("player.joined", function(e)
  local names = {}
  for _, p in ipairs(server.players(e.server)) do names[#names + 1] = p.name end
  log("on:", table.concat(names, ","), server.info(e.server).state)
  server.msg(e.server, e.id, "Welcome, " .. e.name, said("msg"))
  server.teleport(e.server, {e.id}, {x = 1.5, y = 2, z = -3}, said("teleport"))
  server.say(e.server, "Line one\nline two " .. ("x"):rep(300), said("say"))
end)
on("chat", function(e)
  if e.text == "kick" then
    server.kick(e.server, "`+zee+`", said("kick"))
  elseif e.text == "ban" then
    server.ban(e.server, "`+zee+`", said("ban"))
    server.unban(e.server, "`+zee+`", said("unban"))
    server.command(e.server, "frobnicate")
    server.command(e.server, "quit", said("quit"))
  end
end)
on("player.left", function(e) log("left:", e.name, e.reason) end)
`)
	grant(t, m, "acts", "players.chat", "players.kick", "players.ban", "console.exec", "settings.edit")
	enable(t, m, "acts")
	ctx := context.Background()
	in.Command(ctx, "join "+zee+" Zee", "test")
	waitFor(t, func() bool { return hasLine(m, "acts", "say ") })
	for _, w := range []string{"on: Zee running", "msg true Sent to Zee.", "teleport true 1 player(s) teleported to (1.5, 2, -3).", "say true [chat] Server: Line one line two xxx"} {
		if !hasLine(m, "acts", w) {
			t.Errorf("no %q in %q", w, lines(m, "acts"))
		}
	}
	// Chat is cut where the server cuts it.
	for _, e := range in.Console() {
		if e.Kind == logparse.KindInput && strings.HasPrefix(e.Text, "say ") && len(e.Text) != len("say ")+maxChat {
			t.Errorf("said %d bytes: %q", len(e.Text)-len("say "), e.Text)
		}
	}
	m.Notice("s1")(instance.Notice{Kind: instance.NoticeChat, Player: instance.Player{Name: "Zee", ID: zee}, Text: "kick"})
	waitFor(t, func() bool { return hasLine(m, "acts", "left:") && hasLine(m, "acts", "kick ") })
	if !hasLine(m, "acts", "kick true Zee was kicked.") || !hasLine(m, "acts", "left: Zee") {
		t.Errorf("kick: %q", lines(m, "acts"))
	}
	m.Notice("s1")(instance.Notice{Kind: instance.NoticeChat, Player: instance.Player{Name: "Zee", ID: zee}, Text: "ban"})
	waitFor(t, func() bool { return hasLine(m, "acts", "quit ") })
	for _, w := range []string{
		"ban true " + zee + " was banned.",
		"unban true " + zee + " was unbanned.",
		`server.command on s1: Unknown command "frobnicate". Type help.`, // no callback: a failure is logged
		"quit false use the Stop button to stop the server",
	} {
		if !hasLine(m, "acts", w) {
			t.Errorf("no %q in %q", w, lines(m, "acts"))
		}
	}
	if in.State() != instance.Running {
		t.Errorf("the server is %s", in.State())
	}
	by := 0
	for _, e := range in.Console() {
		if e.Kind == logparse.KindInput && e.Name == "plugin:acts" {
			by++
		}
	}
	if by != 7 { // quit never reached it
		t.Errorf("%d console lines by plugin:acts, want 7", by)
	}
	entries, err := m.Store.AuditLog(ctx, "", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var audited []string
	for _, e := range entries {
		if e.Username == "plugin:acts" && e.Instance == "s1" {
			audited = append(audited, e.Action)
		}
	}
	slices.Sort(audited)
	if want := []string{"console.exec", "console.exec", "player.ban", "player.kick", "player.msg", "player.say", "player.teleport", "player.unban"}; !slices.Equal(audited, want) {
		t.Errorf("audited %q, want %q", audited, want)
	}
	if s, _ := m.Get("acts"); s.Stats.Sent != 8 || s.Stats.Errors != 0 {
		t.Errorf("stats %+v", s.Stats)
	}
}

// A plugin that loops on server.say sends a few a second and no more, and
// the panel's own commands still answer at once.
func TestActionsDontHoldUpThePanel(t *testing.T) {
	m := newManager(t)
	reg := servers(t, m, "s1")
	in := fakeServer(t, reg, "s1")
	addPlugin(t, m, "loud", "api = 0\npermissions = [\"players.chat\"]", `
every(0.1, function()
  for i = 1, 100 do server.say("s1", "spam " .. i) end
end)
`)
	grant(t, m, "loud", auth.PlayersChat)
	start := time.Now()
	enable(t, m, "loud")
	for range 10 {
		t0 := time.Now()
		reply, err := in.Command(context.Background(), "status", "owner")
		if err != nil || !strings.HasPrefix(reply, "Fake |") {
			t.Fatalf("status: %q, %v", reply, err)
		}
		if d := time.Since(t0); d > time.Second {
			t.Errorf("the panel's command took %s", d)
		}
		time.Sleep(200 * time.Millisecond)
	}
	s, _ := m.Get("loud")
	if most := uint64(actionBurst + actionRate*time.Since(start).Seconds() + 1); s.Stats.Sent > most || s.Stats.Sent < 4 {
		t.Errorf("sent %d in %s, want at most %d", s.Stats.Sent, time.Since(start), most)
	}
	if s.Stats.Unsent == 0 || s.State != Running {
		t.Errorf("unsent %d, state %s", s.Stats.Unsent, s.State)
	}
	// Turned off, what it hasn't sent stays unsent.
	if err := m.SetEnabled(context.Background(), "loud", false); err != nil {
		t.Fatal(err)
	}
	s, _ = m.Get("loud")
	time.Sleep(time.Second)
	if after, _ := m.Get("loud"); after.Stats.Sent != s.Stats.Sent {
		t.Errorf("sent %d more after it was turned off", after.Stats.Sent-s.Stats.Sent)
	}
}

// A plugin that starts afresh doesn't hear what came of commands its
// earlier Lua sent: those functions are gone.
func TestCallbackAfterAFreshStart(t *testing.T) {
	callTime(t, 50*time.Millisecond)
	m := newManager(t)
	reg := servers(t, m, "s1")
	fakeServer(t, reg, "s1")
	addPlugin(t, m, "spin", "api = 0\npermissions = [\"players.chat\"]", `
log("started")
on("chat", function(e)
  server.say(e.server, "bye", function() log("stale callback ran") end)
  while true do end
end)
`)
	grant(t, m, "spin", auth.PlayersChat)
	enable(t, m, "spin")
	m.Notice("s1")(instance.Notice{Kind: instance.NoticeChat, Player: instance.Player{Name: "Zee"}, Text: "go"})
	waitFor(t, func() bool { s, _ := m.Get("spin"); return s.Restarts == 1 && s.State == Running && s.Stats.Sent == 1 })
	time.Sleep(200 * time.Millisecond)
	if hasLine(m, "spin", "stale callback ran") {
		t.Errorf("a callback from before the fresh start ran: %q", lines(m, "spin"))
	}
	if !hasLine(m, "spin", "starts afresh") {
		t.Errorf("no fresh start: %q", lines(m, "spin"))
	}
}
