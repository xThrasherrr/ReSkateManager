package plugins

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/instance"
	"github.com/xThrasherrr/ReSkateManager/internal/logparse"
	"github.com/xThrasherrr/ReSkateManager/internal/store"
)

func newManager(t *testing.T) *Manager {
	t.Helper()
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	m := &Manager{Dir: filepath.Join(root, "plugins"), Store: st, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), LogPath: filepath.Join(root, "plugins.log")}
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m
}

// addPlugin writes a plugin's folder: plugin.toml from toml (api = 0 when
// empty), main.lua and any other files.
func addPlugin(t *testing.T, m *Manager, id, toml, main string, files ...string) {
	t.Helper()
	dir := filepath.Join(m.Dir, id)
	if toml == "" {
		toml = "api = 0\nversion = \"1.0.0\""
	}
	write := func(name, body string) {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("plugin.toml", toml)
	if main != "" {
		write("main.lua", main)
	}
	for i := 0; i+1 < len(files); i += 2 {
		write(files[i], files[i+1])
	}
}

func enable(t *testing.T, m *Manager, id string) {
	t.Helper()
	if err := m.SetEnabled(context.Background(), id, true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { s, _ := m.Get(id); return s.State != Starting })
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatal("timed out")
}

func lines(m *Manager, id string) []string {
	s, _ := m.Get(id)
	var out []string
	for _, l := range s.Recent {
		out = append(out, l.Text)
	}
	return out
}

func hasLine(m *Manager, id, text string) bool {
	for _, l := range lines(m, id) {
		if strings.Contains(l, text) {
			return true
		}
	}
	return false
}

// callTime sets CallTime for one test. Call it before newManager, so it's
// set back only after the test's plugins have stopped.
func callTime(t *testing.T, d time.Duration) {
	old := CallTime
	t.Cleanup(func() { CallTime = old })
	CallTime = d
}

func joined(name, id string) instance.Notice {
	return instance.Notice{Kind: instance.NoticeJoined, Player: instance.Player{ID: id, Name: name, JoinedAt: time.Now().UnixMilli()}}
}

func TestManifest(t *testing.T) {
	m := newManager(t)
	addPlugin(t, m, "noapi", `name = "No API"`, "log(1)")
	addPlugin(t, m, "future", "api = 1", "log(1)")
	addPlugin(t, m, "nomain", "api = 0", "")
	addPlugin(t, m, "fine", "api = 0\nname = \"Fine\"\nservers = [\"*\"]\nflavour = \"mint\"", "log(1)")
	addPlugin(t, m, "..hidden", "api = 0", "log(1)")
	got := map[string]Status{}
	for _, s := range m.List() {
		got[s.ID] = s
	}
	if len(got) != 4 {
		t.Fatalf("listed %v", got)
	}
	for id, want := range map[string]string{
		"noapi":  "Its plugin.toml doesn't say which plugin API it was made for: add api = 0.",
		"future": "It was made for plugin API 1; this manager runs API 0.",
		"nomain": "It has no main.lua.",
	} {
		if !strings.Contains(got[id].Problem, want) {
			t.Errorf("%s: problem %q", id, got[id].Problem)
		}
	}
	for id, want := range map[string]string{"noapi": "Its plugin.toml", "future": "It was made", "nomain": "It has no"} {
		if !strings.HasPrefix(got[id].Problem, want) {
			t.Errorf("%s: problem %q isn't a sentence", id, got[id].Problem)
		}
	}
	if f := got["fine"]; f.Problem != "" || f.Name != "Fine" || f.Servers != nil || strings.Join(f.Unknown, ",") != "flavour" || f.State != Off {
		t.Errorf("fine: %+v", f)
	}
	enable(t, m, "future")
	if s, _ := m.Get("future"); s.State != Failed || !strings.Contains(s.Reason, "made for plugin API 1") {
		t.Fatalf("future: %+v", s)
	}
}

func TestEvents(t *testing.T) {
	m := newManager(t)
	addPlugin(t, m, "greet", "", `
on("player.joined", function(e) log("hello", e.name, e.id, e.server, math.type(e.joined_at)) end)
on("player.left", function(e) log("bye", e.name, e.reason, e.seconds) end)
on("chat", function(e) log("said", e.name, e.id, e.text) end)
on("state", function(e) log("state", e.was, e.state, e.map) end)
on("throwdown.finished", function(e) log("won", e.mode, e.results[1].name, e.results[1].points, e.results[2].quit) end)
`)
	addPlugin(t, m, "only-b", "api = 0\nservers = [\"b\"]", `on("player.joined", function(e) log("on", e.server) end)`)
	enable(t, m, "greet")
	enable(t, m, "only-b")
	a, b := m.Notice("a"), m.Notice("b")
	a(joined("Zee", "76561198000000001"))
	a(instance.Notice{Kind: instance.NoticeLeft, Player: instance.Player{ID: "76561198000000001", Name: "Zee", JoinedAt: 1000}, Leave: instance.Leave{At: 91000, Reason: "Disconnected."}})
	a(instance.Notice{Kind: instance.NoticeChat, Player: instance.Player{Name: "Bob"}, Text: "gg"})
	a(instance.Notice{Kind: instance.NoticeState, State: instance.Running, Was: instance.Starting, Map: "San Vanelona"})
	a(instance.Notice{Kind: instance.NoticeThrowdown, Throwdown: logparse.Throwdown{Kind: logparse.ThrowdownFinished, Mode: logparse.ModeJam,
		Results: []logparse.Result{{Named: logparse.Named{Name: "Zee"}, Place: 1, Points: 12400}, {Named: logparse.Named{Name: "Bob"}, Place: 2, Quit: true}}}})
	a(instance.Notice{Kind: instance.NoticeMap, Map: "nobody listens"})
	b(joined("Ann", "76561198000000002"))
	want := []string{
		"hello Zee 76561198000000001 a integer",
		"bye Zee Disconnected. 90",
		"said Bob nil gg",
		"state starting running San Vanelona",
		"won jam Zee 12400 true",
		"hello Ann 76561198000000002 b integer",
	}
	waitFor(t, func() bool { return len(lines(m, "greet")) == len(want) })
	if got := lines(m, "greet"); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q", got)
	}
	waitFor(t, func() bool { return len(lines(m, "only-b")) == 1 })
	if got := lines(m, "only-b"); got[0] != "on b" {
		t.Fatalf("only-b: %q", got)
	}
	s, _ := m.Get("greet")
	if s.Stats.Events != 6 || strings.Join(s.Stats.Handlers, ",") != "chat,player.joined,player.left,state,throwdown.finished" {
		t.Fatalf("stats %+v", s.Stats)
	}
}

// A ReSkate plugin's hello and messages, with JSON's whole numbers as Lua's
// integers.
func TestReSkateEvents(t *testing.T) {
	m := newManager(t)
	addPlugin(t, m, "hud", "", `
on("reskate", function(e)
  if e.hello then return log("hello", e.plugin, e.server) end
  log("got", e.plugin, math.type(e.data.n), e.data.n, e.data.won[1], e.data.x, e.data.id, e.hello)
end)`)
	enable(t, m, "hud")
	a := m.Notice("a")
	a(instance.Notice{Kind: instance.NoticeReSkate, Plugin: "races"})
	a(instance.Notice{Kind: instance.NoticeReSkate, Plugin: "races", Text: `{"n":3,"won":["Bea"],"x":1.5,"id":76561198000000001}`})
	want := []string{"hello races a", "got races integer 3 Bea 1.5 76561198000000001 nil"}
	waitFor(t, func() bool { return len(lines(m, "hud")) == len(want) })
	if got := lines(m, "hud"); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q", got)
	}
}

func TestBadCalls(t *testing.T) {
	m := newManager(t)
	addPlugin(t, m, "typo", "", `
log(pcall(on, "player.joind", print))
log(select(2, pcall(function() on("chat", "not a function") end)))
log(select(2, pcall(function() every(0.01, print) end)))
log(select(2, pcall(function() after(-1, print) end)))
log(select(2, pcall(function() require("../secrets") end)))
log(select(2, pcall(function() require("missing") end)))
`)
	enable(t, m, "typo")
	want := []string{
		`false bad argument #1 to 'on' (no event "player.joind")`,
		`main.lua:3: bad argument #2 to 'on' (function expected, got string)`,
		`main.lua:4: bad argument #1 to 'every' (at least 0.1 seconds)`,
		`main.lua:5: bad argument #1 to 'after' (seconds must be from 0 to a week)`,
		`main.lua:6: bad module name "../secrets": use letters, digits and _, with dots between folders`,
		`main.lua:7: no file missing.lua in the plugin's folder`,
	}
	if got := lines(m, "typo"); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got:\n%s", strings.Join(got, "\n"))
	}
}

func TestRequire(t *testing.T) {
	m := newManager(t)
	addPlugin(t, m, "mods", "", `
local util = require("lib.util")
log(util.twice(21), require("lib.util") == util, require("lib.self"))
`, "lib/util.lua", `return {twice = function(n) return n * 2 end}`, "lib/self.lua", `return require("lib.self")`)
	enable(t, m, "mods")
	if got := lines(m, "mods"); strings.Join(got, "|") != "42 true true" {
		t.Fatalf("got %q", got)
	}
}

func TestTimers(t *testing.T) {
	m := newManager(t)
	addPlugin(t, m, "clock", "", `
local ticks = 0
local tick = every(0.1, function() ticks = ticks + 1 log("tick", ticks) end)
after(0, function() log("now") end)
local never = after(0.2, function() log("never") end)
log("cancelled", cancel(never), cancel(never))
after(0.35, function() cancel(tick) log("stopped") end)
`)
	enable(t, m, "clock")
	waitFor(t, func() bool { return hasLine(m, "clock", "stopped") })
	time.Sleep(300 * time.Millisecond)
	got := strings.Join(lines(m, "clock"), "|")
	if !strings.HasPrefix(got, "cancelled true false|now|tick 1|tick 2|tick 3|stopped") || strings.Contains(got, "never") || strings.Contains(got, "tick 4") {
		t.Fatalf("got %q", got)
	}
	if s, _ := m.Get("clock"); s.Stats.Timers != 0 {
		t.Fatalf("timers %d", s.Stats.Timers)
	}
}

// TestRunaway: a handler that runs past its time starts the plugin afresh;
// after overrunLimit of them it's stopped, and says why.
func TestRunaway(t *testing.T) {
	callTime(t, 50*time.Millisecond)
	m := newManager(t)
	addPlugin(t, m, "spin", "", `
log("started")
on("chat", function() while true do pcall(function() while true do end end) end end)
`)
	enable(t, m, "spin")
	chat := m.Notice("a")
	for i := 1; i <= overrunLimit; i++ {
		chat(instance.Notice{Kind: instance.NoticeChat, Player: instance.Player{Name: "Bob"}, Text: "go"})
		if i < overrunLimit {
			waitFor(t, func() bool { s, _ := m.Get("spin"); return s.Restarts == i && s.State == Running })
		}
	}
	waitFor(t, func() bool { s, _ := m.Get("spin"); return s.State == Stopped })
	s, _ := m.Get("spin")
	if !strings.Contains(s.Reason, "ran past 50ms 3 times in 10 minutes") || s.Stats.Memory != 0 {
		t.Fatalf("%+v", s)
	}
	if n := strings.Count(strings.Join(lines(m, "spin"), "|"), "started"); n != overrunLimit {
		t.Fatalf("started %d times", n)
	}
	if got := lines(m, "spin"); strings.Count(strings.Join(got, "|"), "starts afresh") != overrunLimit-1 || got[len(got)-2] != "Its chat handler ran past 50ms." {
		t.Fatalf("log %q", got)
	}
	if !s.Enabled { // still on, so a reload or a restart of the manager tries again
		t.Fatal("turned off")
	}
	// Turned off and on again, it starts afresh, its counts too.
	if err := m.SetEnabled(context.Background(), "spin", false); err != nil {
		t.Fatal(err)
	}
	enable(t, m, "spin")
	if s, _ := m.Get("spin"); s.State != Running || s.Restarts != 0 || s.Stats.Events != 0 || len(s.Failures) != 0 {
		t.Fatalf("after off and on: %+v", s)
	}
}

// TestMemory: a handler that takes all the memory it can fails, and the
// plugin carries on.
func TestMemory(t *testing.T) {
	callTime(t, 5*time.Second) // out of memory, not of time, even under -race
	m := newManager(t)
	addPlugin(t, m, "hog", "", `
on("chat", function(e) if e.text == "hog" then local s = "x" while true do s = s .. s end end log("fine") end)
`)
	enable(t, m, "hog")
	chat := m.Notice("a")
	chat(instance.Notice{Kind: instance.NoticeChat, Player: instance.Player{Name: "Bob"}, Text: "hog"})
	chat(instance.Notice{Kind: instance.NoticeChat, Player: instance.Player{Name: "Bob"}, Text: "hi"})
	waitFor(t, func() bool { return hasLine(m, "hog", "fine") })
	s, _ := m.Get("hog")
	if s.State != Running || len(s.Failures) != 1 || !strings.Contains(s.Failures[0].Text, "chat handler: not enough memory") {
		t.Fatalf("%+v", s)
	}
}

func TestKeepsFailing(t *testing.T) {
	m := newManager(t)
	addPlugin(t, m, "broken", "", `on("chat", function(e) error("broken " .. e.text) end)`)
	enable(t, m, "broken")
	chat := m.Notice("a")
	for i := range failLimit + 5 {
		chat(instance.Notice{Kind: instance.NoticeChat, Player: instance.Player{Name: "Bob"}, Text: string(rune('a' + i))})
	}
	waitFor(t, func() bool { s, _ := m.Get("broken"); return s.State == Stopped })
	s, _ := m.Get("broken")
	if s.Reason != "It failed 20 times in a minute." || s.Stats.Errors != uint64(failLimit) || len(s.Failures) != keepFailures {
		t.Fatalf("%+v", s)
	}
	if !strings.HasPrefix(s.Failures[0].Text, "chat handler: main.lua:1: broken") {
		t.Fatalf("failure %q", s.Failures[0].Text)
	}
}

// TestSlowPlugin: a plugin too slow for its events loses some of them; one
// beside it, on the same events, loses none, and the server never waits.
func TestSlowPlugin(t *testing.T) {
	m := newManager(t)
	addPlugin(t, m, "slow", "", `n = 0 on("chat", function() for i = 1, 300000 do end n = n + 1 end)`)
	addPlugin(t, m, "fast", "", `n = 0 on("chat", function() n = n + 1 end)`)
	enable(t, m, "slow")
	enable(t, m, "fast")
	chat := m.Notice("a")
	start := time.Now()
	const sent = 3000
	for range sent {
		chat(instance.Notice{Kind: instance.NoticeChat, Player: instance.Player{Name: "Bob"}, Text: "spam"})
	}
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Fatalf("sending took %s", took)
	}
	waitFor(t, func() bool {
		s, _ := m.Get("slow")
		f, _ := m.Get("fast")
		return s.Stats.Events+s.Stats.Dropped == sent && f.Stats.Events+f.Stats.Dropped == sent && s.Stats.Calls == s.Stats.Events+1
	})
	s, _ := m.Get("slow")
	f, _ := m.Get("fast")
	if s.Stats.Dropped == 0 {
		t.Fatalf("slow dropped none: %+v", s.Stats)
	}
	if f.Stats.Dropped >= s.Stats.Dropped {
		t.Fatalf("fast dropped %d, slow %d", f.Stats.Dropped, s.Stats.Dropped)
	}
}

// TestReloads: reloading and turning plugins off leaves nothing running:
// not their goroutines, timers or Lua.
func TestReloads(t *testing.T) {
	m := newManager(t)
	addPlugin(t, m, "busy", "", `
every(0.1, function() end)
after(60, function() end)
on("chat", function() end)
co = coroutine.create(function() pcall(coroutine.yield) end)
coroutine.resume(co)
`)
	enable(t, m, "busy")
	runtime.GC()
	before := runtime.NumGoroutine()
	for range 20 {
		if err := m.Reload("busy"); err != nil {
			t.Fatal(err)
		}
		waitFor(t, func() bool { s, _ := m.Get("busy"); return s.State == Running })
	}
	if err := m.SetEnabled(context.Background(), "busy", false); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if after := runtime.NumGoroutine(); after >= before {
		t.Fatalf("goroutines %d with it running, %d after reloads and off", before, after)
	}
	if r := m.running.Load(); r != nil && len(*r) != 0 {
		t.Fatalf("%d runners left", len(*r))
	}
}

// TestEnabledKept: which plugins are on outlasts the manager, and a stop
// while a handler loops doesn't wait for it.
func TestEnabledKept(t *testing.T) {
	m := newManager(t)
	addPlugin(t, m, "loop", "", `on("chat", function() while true do end end)`)
	addPlugin(t, m, "idle", "", `log("up")`)
	addPlugin(t, m, "off", "", `log("never")`)
	enable(t, m, "loop")
	enable(t, m, "idle")
	m.Notice("a")(instance.Notice{Kind: instance.NoticeChat, Player: instance.Player{Name: "Bob"}, Text: "x"})
	time.Sleep(20 * time.Millisecond)
	start := time.Now()
	m.Close()
	if took := time.Since(start); took > CallTime {
		t.Fatalf("close took %s", took)
	}
	m2 := &Manager{Dir: m.Dir, Store: m.Store, Log: m.Log, LogPath: m.LogPath}
	if err := m2.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer m2.Close()
	for _, s := range m2.List() {
		if want := s.ID != "off"; s.Enabled != want {
			t.Fatalf("%s enabled %v", s.ID, s.Enabled)
		}
	}
	b, _ := os.ReadFile(m.LogPath)
	if !strings.Contains(string(b), " idle info: up\n") {
		t.Fatalf("plugins.log: %s", b)
	}
}
