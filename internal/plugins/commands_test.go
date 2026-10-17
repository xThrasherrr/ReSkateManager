package plugins

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/serverconfig"
)

// commands in plugin.toml are words chat commands can have, kept once each.
func TestCommandManifest(t *testing.T) {
	m := newManager(t)
	addPlugin(t, m, "upper", "api = 0\ncommands = [\"Top\"]", "log(1)")
	addPlugin(t, m, "taken", "api = 0\ncommands = [\"status\"]", "log(1)")
	addPlugin(t, m, "fine", "api = 0\ncommands = [\"top\", \"race\", \"top\"]", "log(1)")
	for id, want := range map[string]string{
		"upper": "asks for the chat command /Top, which can't be one",
		"taken": "asks for the chat command /status, which can't be one",
	} {
		if s, _ := m.Get(id); !strings.Contains(s.Problem, want) {
			t.Errorf("%s: problem %q", id, s.Problem)
		}
	}
	if s, _ := m.Get("fine"); s.Problem != "" || strings.Join(s.Commands, ",") != "race,top" {
		t.Errorf("fine: %q, %q", s.Problem, s.Commands)
	}
}

// command() takes one of the words in plugin.toml, and a function.
func TestCommandArguments(t *testing.T) {
	m := newManager(t)
	addPlugin(t, m, "cmds", "api = 0\ncommands = [\"top\"]", `
local calls = {
  function() return command("race", function() end) end,
  function() return command(5, function() end) end,
  function() return command("top", 5) end,
  function() return on("command:top", function() end) end,
  function() command("top", function() end) return "taken" end,
}
for i, f in ipairs(calls) do
  log(i, select(2, pcall(f)))
end
`)
	enable(t, m, "cmds")
	checkLines(t, m, "cmds", []string{
		`bad argument #1 to 'command' (/race isn't among the commands in its plugin.toml)`,
		`bad argument #1 to 'command' (string expected, got number)`,
		`bad argument #2 to 'command' (function expected, got number)`,
		`bad argument #1 to 'on' (no event "command:top")`,
		`5 taken`,
	})
}

// A server starts with the words of the plugins that are on, after the
// owner's own commands and first come first served, and a player's /word
// reaches the plugin it's for with their SteamID, a second apart at most.
// Turned off, a plugin's word goes as the server next starts.
func TestCommands(t *testing.T) {
	callTime(t, 5*time.Second)
	m := newManager(t)
	reg := servers(t, m, "s1")
	in, _ := reg.Get("s1")
	if err := os.WriteFile(in.Def().ConfigPath(), []byte(`{"commands": [{"name": "race", "reply": "The owner's race", "command": [], "admin": false}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	addPlugin(t, m, "boards", "api = 0\ncommands = [\"top\"]\npermissions = [\"players.chat\"]", `
command("top", function(e)
  log("top", e.server, e.id, e.name, #e.args, e.args[1], e.text)
  server.msg(e.server, e.id, "Top of " .. e.args[1])
end)
`)
	addPlugin(t, m, "rival", "api = 0\ncommands = [\"top\", \"race\"]", `
command("top", function(e) log("rival top") end)
command("race", function(e) log("rival race") end)
`)
	grant(t, m, "boards", "players.chat")
	enable(t, m, "boards")
	enable(t, m, "rival")
	fakeServer(t, reg, "s1")
	got := serverconfig.ChatCommands(in.Def().ConfigPath())
	if got["top"].Plugin() != "boards" || got["race"].Plugin() != "" || got["race"].Reply != "The owner's race" || len(got) != 2 {
		t.Fatalf("commands %v", got)
	}
	if !hasLine(m, "rival", "/race isn't on s1: the server's own chat command /race comes first.") ||
		!hasLine(m, "rival", "/top isn't on s1: the plugin boards has it.") {
		t.Errorf("rival: %q", lines(m, "rival"))
	}

	ctx := context.Background()
	run := func(lines ...string) {
		t.Helper()
		for _, l := range lines {
			if _, err := in.Command(ctx, l, "test"); err != nil {
				t.Fatal(err)
			}
		}
	}
	// As the server logs a player's command: typed, then run, then what the
	// commands it ran said (a plugin's runs one, the owner's /race none).
	typed := func(word, rest string) {
		runs := "1"
		if word == "race" {
			runs = "0"
		}
		run("typed " + runs + " Zee /" + word + " " + rest)
	}
	run("join " + zee + " Zee")
	typed("top", "spot week")
	typed("top", "spot week") // within the gap: dropped
	typed("race", "now")
	waitFor(t, func() bool { return hasLine(m, "boards", "msg: ") || hasLine(m, "boards", "top s1") })
	time.Sleep(commandGap)
	typed("top", "jam")
	waitFor(t, func() bool { return strings.Count(strings.Join(lines(m, "boards"), "\n"), "top s1") == 2 })
	time.Sleep(200 * time.Millisecond)
	want := []string{"top s1 " + zee + " Zee 2 spot spot week", "top s1 " + zee + " Zee 1 jam jam"}
	if got := lines(m, "boards"); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("boards: %q, want %q", got, want)
	}
	if hasLine(m, "rival", "rival top") || hasLine(m, "rival", "rival race") {
		t.Errorf("rival heard a command: %q", lines(m, "rival"))
	}
	sent := 0
	for _, e := range in.Console() {
		if strings.HasPrefix(e.Text, "msg "+zee+" Top of ") {
			sent++
		}
	}
	if sent != 2 {
		t.Errorf("%d replies sent, want 2", sent)
	}

	if err := m.SetEnabled(ctx, "boards", false); err != nil {
		t.Fatal(err)
	}
	stopCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := in.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	if err := in.Start(); err != nil {
		t.Fatal(err)
	}
	got = serverconfig.ChatCommands(in.Def().ConfigPath())
	if got["top"].Plugin() != "rival" || got["race"].Plugin() != "" || len(got) != 2 {
		t.Errorf("after boards went off: %v", got)
	}
}
