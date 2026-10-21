package plugins

import (
	"context"
	"strings"
	"testing"
)

// A plugin that takes another's store carries on with what the removed one
// kept, under its prefix, and with its boards; it doesn't start while the
// other is installed, and a key it has already, or one too long with the
// prefix, leaves the other's store where it is.
func TestTakes(t *testing.T) {
	m := newManager(t)
	ctx := context.Background()
	addPlugin(t, m, "cheers", "", `stats.define("cheers", {title = "Cheers"}) store.set("total", 41) store.set("server.s1", 7) log("kept")`)
	enable(t, m, "cheers")
	waitFor(t, func() bool { return hasLine(m, "cheers", "kept") })

	// Defining cheers's board again fails unless it's core's now.
	addPlugin(t, m, "core", "api = 0\ntakes = {cheers = \"cheers.\"}", `
stats.define("cheers", {title = "Cheers"})
local mine = (store.get("mine") or 0) + 1
store.set("mine", mine)
log("has", store.get("cheers.total"), store.get("cheers.server.s1"), store.get("total"), mine)`)
	enable(t, m, "core")
	if s, _ := m.Get("core"); s.State != Failed || !strings.Contains(s.Reason, "remove cheers") {
		t.Fatalf("core with cheers installed: %s %q", s.State, s.Reason)
	}

	// Removed straight after it ran: what it kept may not be written yet.
	if err := m.Remove(ctx, "cheers"); err != nil {
		t.Fatal(err)
	}
	if err := m.Reload("core"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return hasLine(m, "core", "has ") })
	if !hasLine(m, "core", "Took cheers's store: 2 keys.") || !hasLine(m, "core", "has 41 7 nil 1") {
		t.Errorf("core's log: %q", lines(m, "core"))
	}
	if left, _ := m.Store.PluginStore(ctx, "cheers"); len(left) != 0 {
		t.Errorf("cheers's store still has %v", left)
	}
	boards, err := m.Store.Boards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(boards) != 1 || boards[0].Plugin != "core" {
		t.Errorf("boards after the take: %+v, want cheers as core's", boards)
	}

	// Cheers again, removed again: its total is one core has.
	addPlugin(t, m, "cheers", "", `store.set("total", 1) store.set("other", 2) log("kept")`)
	if err := m.Reload("cheers"); err != nil {
		t.Fatal(err)
	}
	enable(t, m, "cheers")
	waitFor(t, func() bool { return hasLine(m, "cheers", "kept") })
	if err := m.Remove(ctx, "cheers"); err != nil {
		t.Fatal(err)
	}
	if err := m.Reload("core"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return hasLine(m, "core", "has 41 7 nil 2") })
	if !hasLine(m, "core", "cheers's store didn't move into this one, and stays as it was: this one's store has cheers.total already") {
		t.Errorf("core's log: %q", lines(m, "core"))
	}
	if left, _ := m.Store.PluginStore(ctx, "cheers"); len(left) != 2 {
		t.Errorf("cheers's store has %v, want its 2 keys", left)
	}

	// A key that the prefix would make longer than store.get takes stays.
	addPlugin(t, m, "wordy", "", `store.set(string.rep("k", 251), 1) log("kept")`)
	enable(t, m, "wordy")
	waitFor(t, func() bool { return hasLine(m, "wordy", "kept") })
	if err := m.Remove(ctx, "wordy"); err != nil {
		t.Fatal(err)
	}
	addPlugin(t, m, "heir", "api = 0\ntakes = {wordy = \"wordy.\"}", `log("up")`)
	enable(t, m, "heir")
	waitFor(t, func() bool { return hasLine(m, "heir", "up") })
	if !hasLine(m, "heir", "would be longer than 256 bytes with the prefix") {
		t.Errorf("heir's log: %q", lines(m, "heir"))
	}
	if left, _ := m.Store.PluginStore(ctx, "wordy"); len(left) != 1 {
		t.Errorf("wordy's store has %v, want its key", left)
	}

	addPlugin(t, m, "self", "api = 0\ntakes = {self = \"\"}", "")
	if err := m.Reload("self"); err != nil {
		t.Fatal(err)
	}
	if s, _ := m.Get("self"); !strings.Contains(s.Problem, "name another plugin") {
		t.Errorf("a plugin taking itself: %q", s.Problem)
	}
}
