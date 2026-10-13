package main

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/xThrasherrr/ReSkateManager/internal/serverconfig"
)

// put sets a dotted path in a config tree, making the objects on the way.
func put(root map[string]any, path string, v any) {
	parts := strings.Split(path, ".")
	for _, p := range parts[:len(parts)-1] {
		next, ok := root[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			root[p] = next
		}
		root = next
	}
	root[parts[len(parts)-1]] = v
}

func TestCompareConfig(t *testing.T) {
	// A config as a server the manager fully knows would write it.
	root := map[string]any{}
	paths := serverconfig.Paths()
	for _, place := range slices.Sorted(maps.Keys(paths)) {
		if fd, ok := serverconfig.FieldByKey(paths[place]); ok {
			put(root, place, fd.Default)
		} else if _, ok := at(root, place); !ok {
			put(root, place, map[string]any{})
		}
	}
	put(root, "players.new_thing", true)                     // new
	delete(root["players"].(map[string]any), "allow_noclip") // gone
	put(root, "network.send_rate", 1000)                     // another default
	put(root, "network.pack_ms", 999)                        // past the manager's limit

	data, _ := json.Marshal(root)
	file := filepath.Join(t.TempDir(), "ReSkateServer.json")
	if err := os.WriteFile(file, data, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := serverconfig.Read(file)
	if err != nil {
		t.Fatal(err)
	}
	r := &report{}
	r.compareConfig(f)
	check := func(what string, items []string, want ...string) {
		t.Helper()
		if len(items) != len(want) {
			t.Errorf("%s = %q, want %d", what, items, len(want))
			return
		}
		for i, w := range want {
			if !strings.Contains(items[i], w) {
				t.Errorf("%s[%d] = %q, want it to name %q", what, i, items[i], w)
			}
		}
	}
	check("Adds", r.Adds, "players.new_thing", "network.send_rate")
	check("Breaking", r.Breaking, "players.allow_noclip", "network.pack_ms")
	// Where the server writes each setting, and the manager's names for them.
	for key, want := range map[string]bool{"players.new_thing": true, "network.send_rate": true, "send_rate": true, "noclip": false} {
		if r.written[key] != want {
			t.Errorf("written[%q] = %v", key, !want)
		}
	}
}

func TestDescribe(t *testing.T) {
	r := &report{cur: &upstream{readme: "party_size         Most players.\n" +
		"afk_kick_minutes   Remove a player who has\n" +
		"                   been away.\n" +
		"sync_effects       Effects.\n"}}
	if got, want := r.describe("players.afk_kick_minutes"), ": `\"Remove a player who has been away.\"`"; got != want {
		t.Errorf("describe = %s, want %s", got, want)
	}
	if got := r.describe("players.unknown"); got != "" {
		t.Errorf("describe(unknown) = %q", got)
	}

	// Under its section's heading, and in another setting's text: 2.0.8's
	// votes.kick_minutes was given players.kick_minutes' words. Without a
	// heading, the first of its name; a vote's own is among the others' words.
	r.cur.readme = "\"players\" - What players may do.\n" +
		"kick_minutes       How long a player kicked by an admin\n" +
		"                   is kept out.\n" +
		"\n" +
		"\"votes\" - What players may vote on.\n" +
		"votes              Player votes:\n" +
		"                   needs two. \"kick_minutes\" (default 5) is how long a\n" +
		"                   player kicked by a vote is kept out.\n" +
		"                   Each vote may also have its own \"seconds\".\n" +
		"seconds            How long a vote runs.\n"
	for place, want := range map[string]string{
		"players.kick_minutes":  ": `\"How long a player kicked by an admin is kept out.\"`",
		"votes.kick_minutes":    ": `\"\\\"kick_minutes\\\" (default 5) is how long a player kicked by a vote is kept out.\"`",
		"votes.seconds":         ": `\"How long a vote runs.\"`",
		"votes.map.seconds":     "",
		"network.kick_minutes":  ": `\"How long a player kicked by an admin is kept out.\"`",
		"players.allow_seconds": "",
	} {
		if got := r.describe(place); got != want {
			t.Errorf("describe(%s) = %s, want %s", place, got, want)
		}
	}
}

// The Windows run, which has no source, finds the same new settings without
// the README's words: each is listed once, with them.
func TestIncludeSaysMore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "windows.json")
	data, _ := json.Marshal(findings{Adds: []string{"New setting `\"players.kick_minutes\"`, default `0`", "New command `\"kick-time\"`"}})
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	r := &report{Adds: []string{"New setting `\"players.kick_minutes\"`, default `0`: `\"How long.\"`", "New command `\"kick\"`"}}
	r.include(path)
	want := []string{"New setting `\"players.kick_minutes\"`, default `0`: `\"How long.\"`", "New command `\"kick\"`", "New command `\"kick-time\"`"}
	if !slices.Equal(r.Adds, want) {
		t.Errorf("adds\n got %q\nwant %q", r.Adds, want)
	}
}

func TestTitle(t *testing.T) {
	r := &report{To: "v2.0.0"}
	if got := r.title(); got != "ReSkate v2.0.0: nothing to apply" {
		t.Errorf("title = %q", got)
	}
	r.Breaking, r.Adds = []string{"a"}, []string{"b", "c"}
	if got := r.title(); got != "ReSkate v2.0.0: 1 breaking, 2 new" {
		t.Errorf("title = %q", got)
	}
	if md := r.markdown(); !strings.HasPrefix(md, "<!-- reskate-watch v2.0.0 -->\n") || strings.Contains(md, "reskate-watch-built") {
		t.Errorf("markdown doesn't start with the release's marker alone:\n%s", md)
	}
	// Files uploaded again after the release was checked: the same marker,
	// what the source was compared as, which the workflow's next check of
	// them compares from, and a title of its own.
	r.rebuilt, r.From, r.fromRef, r.toRef = true, "v2.0.0", "v2.0.0", "d2b8e180973b1c997a4329afda3e0cef8300fdb2"
	if got := r.title(); got != "ReSkate v2.0.0, rebuilt: 1 breaking, 2 new" {
		t.Errorf("title = %q", got)
	}
	if md := r.markdown(); !strings.HasPrefix(md, "<!-- reskate-watch v2.0.0 -->\n<!-- reskate-watch-built d2b8e180973b1c997a4329afda3e0cef8300fdb2 -->\n") ||
		!strings.Contains(md, "built from d2b8e18 after the tag") ||
		!strings.Contains(md, "/compare/v2.0.0...d2b8e180973b1c997a4329afda3e0cef8300fdb2") {
		t.Errorf("markdown:\n%s", md)
	}
}

func TestCode(t *testing.T) {
	for s, want := range map[string]string{"plain": "`\"plain\"`", "a`b": "``\"a`b\"``", "two\nlines": "`\"two\\nlines\"`"} {
		if got := code(s); got != want {
			t.Errorf("code(%q) = %s, want %s", s, got, want)
		}
	}
}
