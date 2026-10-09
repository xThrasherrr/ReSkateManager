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
	if !strings.HasPrefix(r.markdown(), "<!-- reskate-watch v2.0.0 -->\n") {
		t.Errorf("markdown doesn't start with the release's marker")
	}
}

func TestCode(t *testing.T) {
	for s, want := range map[string]string{"plain": "`\"plain\"`", "a`b": "``\"a`b\"``", "two\nlines": "`\"two\\nlines\"`"} {
		if got := code(s); got != want {
			t.Errorf("code(%q) = %s, want %s", s, got, want)
		}
	}
}
