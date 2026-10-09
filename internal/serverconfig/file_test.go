package serverconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const sample = `{
  "name": "ReSkate server",
  "map": "San Vansterdam",
  "max_players": 16,
  "password": "",
  "welcome": "",
  "listed": true,
  "auto_update": true,
  "tps": 30,
  "port": 27015,
  "query_port": 27016,
  "voice_chat": true,
  "voice_range": 300.0,
  "distances": {"full_rate_return": 50, "half_rate_start": 60, "half_rate_return": 150, "low_rate_start": 170},
  "object_placement": "everyone",
  "votes": {"map": {"enabled": false, "percent": 60}, "kick": {"enabled": false, "percent": 60},
            "time_of_day": {"enabled": false, "percent": 50}, "seconds": 30, "cooldown_seconds": 60},
  "parks": {"construction": "skatepark_01", "historic": "megapark_05", "financial": "flumppark_08"},
  "score_allow": ["00000000000000aa"],
  "admins": ["76561198000000001"],
  "bans": [{"id": "76561198000000002", "name": "Griefer", "added": 1700000000}],
  "future_key": {"x": 1}
}`

func load(t *testing.T) (*File, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ReSkateServer.json")
	if err := os.WriteFile(path, []byte(sample), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	return f, path
}

func TestDiffCommands(t *testing.T) {
	f, _ := load(t)
	p, err := Diff(f, map[string]any{
		"name":                      "[EU] Thrasher Park",
		"password":                  "", // unchanged
		"send_rate":                 1100.0,
		"crowd_budget":              0.0,
		"reserved":                  []any{"76561198000000003"},
		"distances.low_rate_start":  200.0, // one of four → whole distances command
		"votes.kick.enabled":        true,
		"votes.time_of_day.percent": 70.0,
		"parks.historic":            "empty",
		"score_allow":               []any{"00000000000000BB"},
		"max_players":               24.0,
		"map_rotation_minutes":      15.0,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"name [EU] Thrasher Park",
		"reserved add 76561198000000003",
		"rotation 15",
		"rate 1100",
		"crowd off",
		"distances 50 60 150 200",
		"park historic empty",
		"votes kick on",
		"votes tod 70",
		"score-allow remove 00000000000000aa",
		"score-allow 00000000000000bb",
	}
	if !slices.Equal(p.Commands, want) {
		t.Errorf("commands\n got %q\nwant %q", p.Commands, want)
	}
	if p.Restart["max_players"] != 24.0 || len(p.Restart) != 1 {
		t.Errorf("restart %v", p.Restart)
	}
	// The running server takes reserved slots only below its own max players.
	p, err = Diff(f, map[string]any{"reserved_slots": 2.0}, nil)
	if err != nil || !slices.Equal(p.Commands, []string{"reserved slots 2"}) {
		t.Errorf("reserved_slots: %v %v", p, err)
	}
	p, err = Diff(f, map[string]any{"reserved_slots": 20.0, "max_players": 24.0}, nil)
	if err != nil || len(p.Commands) != 0 || p.Restart["reserved_slots"] != 20.0 {
		t.Errorf("reserved_slots with max_players: %v %v", p, err)
	}
}

func TestDiffValidation(t *testing.T) {
	f, _ := load(t)
	for _, bad := range []map[string]any{
		{"distances.full_rate_return": 100.0},
		{"tps": "60"}, // fixed at 20 since 1.1.5
		{"max_players": 0.0},
		{"voice_range": 10.0},
		{"name": "two\nlines"},
		{"name": "Thrasher's Park"},
		{"name": " "},
		{"port": 27016.0},
		{"score_allow": []any{"xyz"}},
		{"map_rotation_minutes": 1441.0},
		{"map_pool": []any{"Isle of Grom", "Nowhere"}},
		{"map_pool": "Isle of Grom"},
		{"steam_token": "not a token"},
		{"steam_token": "ABC-123"},
		{"steam_token": strings.Repeat("A", 65)},
		{"object_limit": 1025.0},
		{"object_limit": -1.0},
		{"send_rate": 127.0},
		{"send_rate": 16385.0},
		{"crowd_budget": 299.0},
		{"crowd_budget": 20001.0},
		{"bone_scale_limit": 0.5},
		{"bone_scale_limit": 8.5},
		{"bone_scale_limit": -1.0},
		{"reserved_slots": 16.0},
		{"reserved_slots": 4.0, "max_players": 4.0},
		{"reserved": []any{"76561198"}},
		{"reserved": []any{"Thrasher"}},
		{"reserved": manyIDs(1025)},
		{"nope": 1},
	} {
		if _, err := Diff(f, bad, RetailMaps); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	p, err := Diff(f, map[string]any{"password": "secret", "welcome": ""}, nil)
	if err != nil || !slices.Equal(p.Commands, []string{"password secret"}) {
		t.Errorf("password: %v %v", p, err)
	}
	// No command sets it, so it is written while the server is stopped.
	p, err = Diff(f, map[string]any{"steam_token": " 0123456789ABCDEF0123456789abcdef\t"}, nil)
	if err != nil || len(p.Commands) != 0 || p.Restart["steam_token"] != "0123456789ABCDEF0123456789abcdef" {
		t.Errorf("steam_token: %v %v", p, err)
	}
	p, err = Diff(f, map[string]any{"object_limit": 0.0, "global_bans": false}, nil)
	if err != nil || !slices.Equal(p.Commands, []string{"objects off"}) || p.Restart["global_bans"] != false {
		t.Errorf("object_limit and global_bans: %v %v", p, err)
	}
	for v, want := range map[float64]string{0: "bone-scale off", 1.5: "bone-scale 1.5", 8: "bone-scale 8"} {
		p, err = Diff(f, map[string]any{"bone_scale_limit": v}, nil)
		if err != nil || !slices.Equal(p.Commands, []string{want}) {
			t.Errorf("bone_scale_limit %g: %v %v", v, p, err)
		}
	}
	p, err = Diff(f, map[string]any{"crowd_budget": 300.0, "reserved": manyIDs(1024)}, nil)
	if err != nil || len(p.Commands) != 1025 || p.Commands[1024] != "crowd 300" {
		t.Errorf("crowd_budget and a full reserved list: %v", err)
	}
}

func manyIDs(n int) []any {
	ids := make([]any, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("765611980%08d", i)
	}
	return ids
}

// What someone who isn't an owner brings in keeps the owner-only settings
// the server had.
func TestKeepOwnerOnly(t *testing.T) {
	f := &File{Root: map[string]any{"name": "New", "steam_token": "THEIRS"}}
	f.KeepOwnerOnly(&File{Root: map[string]any{"name": "Old", "steam_token": "MINE"}})
	if f.Root["steam_token"] != "MINE" || f.Root["name"] != "New" {
		t.Errorf("kept %v", f.Root)
	}
	f.KeepOwnerOnly(&File{Root: map[string]any{}})
	if _, ok := f.Root["steam_token"]; ok {
		t.Errorf("a token the server didn't have: %v", f.Root)
	}
	f = &File{Root: map[string]any{"name": "A", "steam_token": "MINE"}}
	if f.Redact(); len(f.Root) != 1 {
		t.Errorf("redacted %v", f.Root)
	}
}

func TestOfflineWriteKeepsUnknownKeys(t *testing.T) {
	f, path := load(t)
	p, err := Diff(f, map[string]any{"max_players": 32.0, "send_rate": 1100.0, "map_pool": []any{"stadium 1", "Isle of Grom"}}, RetailMaps)
	if err != nil {
		t.Fatal(err)
	}
	f.ApplyOffline(p.Changed)
	if err := f.Write(path); err != nil {
		t.Fatal(err)
	}
	g, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	v := g.Values()
	if v["max_players"] != 32.0 || v["send_rate"] != 1100.0 {
		t.Errorf("values %v %v", v["max_players"], v["send_rate"])
	}
	// Spelled as the server lists the maps.
	if pool := v["map_pool"]; !slices.Equal(pool.([]string), []string{"Stadium 1", "Isle of Grom"}) {
		t.Errorf("map pool %q", pool)
	}
	if _, ok := g.Get("future_key.x"); !ok {
		t.Error("unknown key dropped")
	}
	if tps, _ := g.Get("tps"); fmt.Sprint(tps) != "30" {
		t.Errorf("tps, no longer a setting, is %v", tps)
	}
	if b := g.Bans(); len(b) != 1 || b[0].Name != "Griefer" || b[0].Added != 1700000000 {
		t.Errorf("bans %+v", b)
	}
	if a := g.Admins(); len(a) != 1 || a[0] != "76561198000000001" {
		t.Errorf("admins %v", a)
	}
}

// Every command Diff plans is in SettingVerbs, so the console can't be used
// to change a setting without the permission the settings pages need.
func TestSettingVerbsCoverDiff(t *testing.T) {
	f, _ := load(t)
	cur := f.Values()
	for _, fd := range Fields {
		if fd.Restart {
			continue
		}
		var tries []any
		switch v := cur[fd.Key].(type) {
		case bool:
			tries = []any{!v}
		case float64:
			tries = []any{v + 1, v - 1, v + 10}
		case string:
			tries = []any{"x", "#123456"}
			for _, o := range fd.Options {
				if o != v {
					tries = append(tries, o)
				}
			}
		case []string:
			tries = []any{[]any{"00000000000000cc"}, []any{"Isle of Grom"}, []any{"76561198000000003"}}
		case []CustomVote:
			// Only a vote the server has changes live.
			f.Set(fd.Key, []any{map[string]any{"name": "restart", "command": "map {map}"}})
			tries = []any{[]any{map[string]any{"name": "restart", "command": "map {map}", "enabled": false}}}
		}
		var p *Plan
		for _, try := range tries {
			if p, _ = Diff(f, map[string]any{fd.Key: try}, RetailMaps); p != nil && len(p.Commands) > 0 {
				break
			}
		}
		if p == nil || len(p.Commands) == 0 {
			t.Errorf("%s: found no change to plan", fd.Key)
			continue
		}
		for _, cmd := range p.Commands {
			if verb, _, _ := strings.Cut(cmd, " "); !slices.Contains(SettingVerbs, verb) {
				t.Errorf("%s plans %q, and %q is not in SettingVerbs", fd.Key, cmd, verb)
			}
		}
	}
}

// poolServer follows Host::command's map-pool add|remove|clear, refusing what
// the server refuses.
type poolServer struct{ all, pool []string }

func (s *poolServer) run(cmd string) error {
	what, m, _ := strings.Cut(strings.TrimPrefix(cmd, "map-pool "), " ")
	listed := slices.Contains(s.pool, m)
	switch {
	case what == "clear":
		s.pool = nil
	case !slices.Contains(s.all, m):
		return fmt.Errorf("%s: no such map", cmd)
	case what == "add":
		if listed || len(s.pool) == 0 {
			return fmt.Errorf("%s: already in the pool", cmd)
		}
		s.pool = append(s.pool, m)
	case what == "remove":
		if len(s.pool) == 0 {
			s.pool = slices.Clone(s.all)
		} else if !listed {
			return fmt.Errorf("%s: not in the pool", cmd)
		}
		if len(s.pool) == 1 {
			return fmt.Errorf("%s: the pool needs a map", cmd)
		}
		s.pool = slices.DeleteFunc(s.pool, func(x string) bool { return x == m })
	default:
		return fmt.Errorf("%s: not a pool command", cmd)
	}
	return nil
}

// Every pool the server can hold turns into every other one.
func TestPoolCommands(t *testing.T) {
	all := []string{"San Vansterdam", "Isle of Grom", "Stadium 1", "bbcity"}
	var pools [][]string // every ordered pick of all, the empty one too
	var pick func(cur []string)
	pick = func(cur []string) {
		pools = append(pools, slices.Clone(cur))
		for _, m := range all {
			if !slices.Contains(cur, m) {
				pick(append(cur, m))
			}
		}
	}
	pick(nil)
	for _, from := range pools {
		for _, want := range pools {
			cmds, err := poolCommands(from, want, all)
			if err != nil {
				t.Fatalf("%q to %q: %v", from, want, err)
			}
			s := &poolServer{all: all, pool: slices.Clone(from)}
			for _, cmd := range cmds {
				if err := s.run(cmd); err != nil {
					t.Fatalf("%q to %q: %v (plan %q)", from, want, err, cmds)
				}
			}
			if !slices.Equal(s.pool, want) {
				t.Fatalf("%q to %q ended at %q (plan %q)", from, want, s.pool, cmds)
			}
		}
	}
	// The usual edits stay short.
	for _, c := range []struct{ from, want, cmds []string }{
		{nil, []string{"Isle of Grom"}, []string{"map-pool remove San Vansterdam", "map-pool remove Stadium 1", "map-pool remove bbcity"}},
		{[]string{"Isle of Grom"}, []string{"Isle of Grom", "bbcity"}, []string{"map-pool add bbcity"}},
		{[]string{"Isle of Grom", "bbcity", "Stadium 1"}, []string{"Isle of Grom", "Stadium 1"}, []string{"map-pool remove bbcity"}},
		{[]string{"Isle of Grom", "bbcity"}, []string{"bbcity", "Isle of Grom"}, []string{"map-pool remove Isle of Grom", "map-pool add Isle of Grom"}},
		{[]string{"Isle of Grom"}, []string{"bbcity"}, []string{"map-pool add bbcity", "map-pool remove Isle of Grom"}},
		{[]string{"bbcity"}, nil, []string{"map-pool clear"}},
	} {
		if cmds, _ := poolCommands(c.from, c.want, all); !slices.Equal(cmds, c.cmds) {
			t.Errorf("%q to %q:\n got %q\nwant %q", c.from, c.want, cmds, c.cmds)
		}
	}
	if _, err := poolCommands(nil, []string{"bbcity"}, nil); err == nil {
		t.Error("narrowed every map without the server's maps")
	}
}

// The cases of Server/Test/server_config_tests.cpp.
func TestValidServerName(t *testing.T) {
	for _, good := range []string{"Old Server", "[EU] Skate_Park-2 (24x7)", "a", "EU/West 24/7"} {
		if !ValidServerName(good) {
			t.Errorf("%q refused", good)
		}
	}
	for _, bad := range []string{"", strings.Repeat("a", 65), "Best! Server", "café", "a.b", "<b>x</b>",
		" padded", "padded ", "[]--()", "two\nlines", "///", `a\b`} {
		if ValidServerName(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestServerName(t *testing.T) {
	for in, want := range map[string]string{
		"Old Server":                     "Old Server",
		"Thrasher's Park!":               "Thrashers Park",
		"  Café & Bar  ":                 "Caf Bar",
		`EU/West 24/7 \o/`:               "EU/West 24/7 o/",
		"!!!":                            "ReSkate server",
		"[]":                             "ReSkate server",
		strings.Repeat("a", 70):          strings.Repeat("a", 64),
		strings.Repeat("a", 63) + " bcd": strings.Repeat("a", 63), // the cut ends on the space
	} {
		if got := ServerName(in); got != want || !ValidServerName(got) {
			t.Errorf("ServerName(%q) = %q, want %q", in, got, want)
		}
	}
}

// A config is one JSON object: null, or more after it, is refused rather than
// read as settings that then panic on the first change.
func TestReadRefusesNonObjects(t *testing.T) {
	dir := t.TempDir()
	for body, ok := range map[string]bool{
		`{"name":"A"}`:              true,
		"{\"name\":\"A\"}\n\n":      true,
		`null`:                      false,
		`{"name":"A"} {"name":"B"}`: false,
		`{"name":"A"} trailing`:     false,
		`[1,2]`:                     false,
	} {
		path := filepath.Join(dir, "ReSkateServer.json")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		f, err := Read(path)
		if (err == nil) != ok {
			t.Errorf("Read(%q) = %v", body, err)
		}
		if err == nil {
			f.Set("name", "B") // must not panic
		}
	}
}

// Text settings go to the server's console, so control characters are out;
// the welcome message follows the server's own chat rule.
func TestTextSettingRules(t *testing.T) {
	f := &File{Root: map[string]any{"name": "A"}}
	for values, ok := range map[string]bool{
		"welcome=Hi there":                true,
		"welcome=":                        true,
		"welcome=   ":                     false, // only spaces
		"welcome=tab\there":               false,
		"welcome=ctrl-z\x1a":              false,
		"password=hunter2":                true,
		"password=hunter\x002":            false,
		"password=c1\u0085":               false,
		"map=San Vansterdam":              true,
		"map=" + strings.Repeat("x", 257): false,
	} {
		key, value, _ := strings.Cut(values, "=")
		if _, err := Diff(f, map[string]any{key: value}, nil); (err == nil) != ok {
			t.Errorf("%s=%q: %v", key, value, err)
		}
	}
}
