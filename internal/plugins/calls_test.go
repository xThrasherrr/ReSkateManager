package plugins

import (
	"context"
	"strings"
	"testing"
)

// A plugin calls what another exposes and hears the answer, or why there's
// none: it failed, it can't be copied back, or there's no such plugin or name.
func TestPluginCalls(t *testing.T) {
	m := newManager(t)
	addPlugin(t, m, "a", "", `
expose("top", function(e) return {n = e.args.n + 1, from = e.from} end)
expose("none", function(e) end)
expose("boom", function(e) error("nope") end)
expose("bad", function(e) return print end)
log("up")
`)
	addPlugin(t, m, "b", "", `
local function heard(what)
  return function(ok, v)
    log(what, ok, type(v) == "table" and (v.n .. " from " .. v.from) or tostring(v))
  end
end
log("top", call("a", "top", {n = 1}, heard("top:")))
log("none", call("a", "none", nil, heard("none:")))
log("boom", call("a", "boom", 1, heard("boom:")))
log("bad", call("a", "bad", 1, heard("bad:")))
log("missing", call("a", "missing", 1))
log("off", call("zz", "top", 1))
log("args", pcall(call, "a", "top", print))
log("expose", pcall(expose, "x", 1))
log("twice", pcall(function() expose("y", function() end) expose("y", function() end) end))
`)
	enable(t, m, "a")
	waitFor(t, func() bool { return hasLine(m, "a", "up") })
	enable(t, m, "b")
	waitFor(t, func() bool { return hasLine(m, "b", "bad: ") })
	for _, w := range []string{
		"top true", "none true", "boom true", "bad true",
		"missing false a doesn't expose missing",
		"off false zz isn't running",
		"args false", "bad argument #3 to 'call' (can't send a function)",
		"expose false", "bad argument #2 to 'expose' (function expected, got number)",
		"twice false", "bad argument #1 to 'expose' (it exposes y already)",
		"top: true 2 from b",
		"none: true nil",
		"boom: false a's boom failed: its log says why",
		"bad: false a's bad failed: its log says why",
	} {
		if !hasLine(m, "b", w) {
			t.Errorf("b: no %q in %q", w, lines(m, "b"))
		}
	}
	waitFor(t, func() bool { return hasLine(m, "a", "its answer to b") })
	for _, w := range []string{"call boom: ", "nope", "its answer to b can't go back: can't send a function"} {
		if !hasLine(m, "a", w) {
			t.Errorf("a: no %q in %q", w, lines(m, "a"))
		}
	}
}

// Every call gets one answer, even from a plugin turned off with calls still
// queued, so the caller's count of calls waiting comes back to none.
func TestPluginCallsToAStoppedPlugin(t *testing.T) {
	m := newManager(t)
	addPlugin(t, m, "a", "", `
expose("slow", function(e) for _ = 1, 200000 do end return e.args end)
log("up")
`)
	addPlugin(t, m, "b", "", `
local got = 0
after(0, function()
  for i = 1, 64 do
    call("a", "slow", i, function(ok, v)
      got = got + 1
      log("answer", got, ok, v)
    end)
  end
  log("queued")
end)
`)
	enable(t, m, "a")
	waitFor(t, func() bool { return hasLine(m, "a", "up") })
	enable(t, m, "b")
	waitFor(t, func() bool { return hasLine(m, "b", "queued") })
	if err := m.SetEnabled(context.Background(), "a", false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return hasLine(m, "b", "answer 64 ") })
	waiting := func() int32 {
		m.mu.Lock()
		p := m.plugins["b"]
		m.mu.Unlock()
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.run.calls.Load()
	}
	waitFor(t, func() bool { return waiting() == 0 })
	refused := 0
	for _, l := range lines(m, "b") {
		if strings.HasPrefix(l, "answer 65 ") {
			t.Errorf("answered twice: %q", l)
		}
		if strings.HasSuffix(l, "false a stopped") {
			refused++
		}
	}
	if refused == 0 {
		t.Errorf("a answered every call before it stopped: %q", lines(m, "b"))
	}
}
