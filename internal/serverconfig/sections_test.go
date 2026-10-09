package serverconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A config as ReSkate 1.1.7 and later write it (layout in Server/server_config.cpp).
const sectionedConfig = `{
  "server": {"name": "Sectioned", "password": "", "welcome_message": "hi", "listed": true, "max_players": 32,
    "port": 27017, "query_port": 27018, "steam_token": "TOKEN", "auto_update": false, "activity_log": true},
  "access": {"admins": ["76561198000000001"], "reserved_players_slots": ["76561198000000002"], "use_global_bans": false},
  "maps": {"map": "Isle of Grom", "pool": ["Isle of Grom", "San Vansterdam"], "rotation_minutes": 20,
    "parks": {"construction": "skatepark_01", "historic": "empty", "financial": "empty"}, "world_layer_sync": true,
    "layers": {"grom_tod_1_night": "on"}},
  "players": {"allow_boosts": false, "allow_no_bail": true, "allow_noclip": false, "allow_parties": true, "party_size": 4,
    "afk_kick_minutes": 5, "allow_voice_chat": true, "voice_range": 30, "object_placement": "admins", "object_limit": 10,
    "allow_object_scaling": true, "sync_effects": true, "announce_throwdowns": false},
  "anti_cheat": {"speed_hack": "kick", "modified_scoring": "off", "allowed_scoring_mods": ["00000000deadbeef"],
    "enforce_tuning": true, "bone_scale_limit": 2},
  "network": {"use_steam_relay": true, "send_rate": 48, "crowd_budget": 0, "pack_ms": 10, "finger_distance": 25,
    "distances": {"full_rate_return": 20, "half_rate_start": 30, "half_rate_return": 40, "low_rate_start": 60}, "steam_debug": false},
  "votes": {"map": {"enabled": true, "percent": 60}, "kick": {"enabled": false, "percent": 70},
    "time_of_day": {"enabled": true, "percent": 50}, "seconds": 30, "cooldown_seconds": 120}
}`

func readSectioned(t *testing.T) (*File, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ReSkateServer.json")
	if err := os.WriteFile(path, []byte(sectionedConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	return f, path
}

// Every setting is found in its section, under its new name where it has one.
func TestSectionedValues(t *testing.T) {
	f, _ := readSectioned(t)
	if !f.Sectioned() {
		t.Fatal("not seen as sectioned")
	}
	for _, fd := range Fields {
		if _, ok := f.Get(fd.Key); !ok && !slices.Contains(Removed, fd.Key) {
			t.Errorf("%s not found", fd.Key)
		}
	}
	v := f.Values()
	for key, want := range map[string]any{
		"name": "Sectioned", "welcome": "hi", "no_bail": true, "boosts": false, "global_bans": false,
		"speed_check": "kick", "score_check": "off", "map_rotation_minutes": 20.0, "port": 27017.0,
		"parks.construction": "skatepark_01", "distances.low_rate_start": 60.0, "votes.kick.percent": 70.0,
		"object_placement": "admins", "steam_token": "TOKEN",
	} {
		if v[key] != want {
			t.Errorf("%s = %v, want %v", key, v[key], want)
		}
	}
	if !slices.Equal(v["map_pool"].([]string), []string{"Isle of Grom", "San Vansterdam"}) ||
		!slices.Equal(v["reserved"].([]string), []string{"76561198000000002"}) ||
		!slices.Equal(v["score_allow"].([]string), []string{"00000000deadbeef"}) {
		t.Errorf("lists: %v %v %v", v["map_pool"], v["reserved"], v["score_allow"])
	}
	if a := f.Admins(); !slices.Equal(a, []string{"76561198000000001"}) {
		t.Errorf("admins %v", a)
	}
	if l := f.Layers(); l["grom_tod_1_night"] != "on" {
		t.Errorf("layers %v", l)
	}
}

// A change to a sectioned file goes into its section, as the server reads
// it first, and a copy left at the top (by 1.0.0) goes.
func TestSectionedWrites(t *testing.T) {
	f, _ := readSectioned(t)
	f.Root["no_bail"] = false // as 1.0.0 wrote it
	f.ApplyOffline(map[string]any{"no_bail": false, "map_pool": []string{"San Vansterdam"}, "distances.full_rate_return": 25.0})
	f.SetAdmins([]string{"76561198000000003"})
	for path, want := range map[string]string{"players.allow_no_bail": `false`, "maps.pool": `["San Vansterdam"]`,
		"network.distances.full_rate_return": `25`, "access.admins": `["76561198000000003"]`} {
		got, ok := f.at(path)
		if b, _ := json.Marshal(got); !ok || string(b) != want {
			t.Errorf("%s = %s, want %s", path, b, want)
		}
	}
	for _, top := range []string{"no_bail", "allow_no_bail", "map_pool", "pool", "admins", "distances"} {
		if _, ok := f.Root[top]; ok {
			t.Errorf("%s left at the top", top)
		}
	}

	// A flat file stays flat, under the names an older server reads.
	flat := &File{Root: map[string]any{"name": "Flat", "no_bail": true}}
	flat.ApplyOffline(map[string]any{"no_bail": false})
	flat.SetAdmins([]string{"76561198000000003"})
	if flat.Sectioned() || flat.Root["no_bail"] != false || flat.Root["admins"] == nil {
		t.Errorf("flat file became %v", flat.Root)
	}
}

// Someone who isn't an owner never gets the Steam token in an export, in
// either layout.
func TestSectionedRedact(t *testing.T) {
	f, _ := readSectioned(t)
	f.Root["steam_token"] = "LEFT AT THE TOP"
	f.Redact()
	if _, ok := f.Get("steam_token"); ok {
		t.Errorf("token kept: %v", f.Root["server"])
	}
	f.KeepOwnerOnly(&File{Root: map[string]any{"server": map[string]any{"steam_token": "MINE"}}})
	if v, _ := f.Get("steam_token"); v != "MINE" {
		t.Errorf("token put back as %v", v)
	}
}

// 1.1.7's network settings have no console command: they wait for the
// server to stop, and go into its network section.
func TestNetworkSettings(t *testing.T) {
	f, _ := readSectioned(t)
	p, err := Diff(f, map[string]any{"use_steam_relay": false, "pack_ms": 5.0, "finger_distance": 40.0, "steam_debug": true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Commands) != 0 || len(p.Restart) != 4 {
		t.Fatalf("planned %+v", p)
	}
	f.ApplyOffline(p.Restart)
	for path, want := range map[string]string{"network.use_steam_relay": `false`, "network.pack_ms": `5`,
		"network.finger_distance": `40`, "network.steam_debug": `true`} {
		got, ok := f.at(path)
		if b, _ := json.Marshal(got); !ok || string(b) != want {
			t.Errorf("%s = %s, want %s", path, b, want)
		}
	}
	if _, err := Diff(f, map[string]any{"pack_ms": 51.0}, nil); err == nil {
		t.Error("pack_ms 51 accepted; the server allows 0 to 50")
	}
}

// 2.0.0's player settings change live, and go into its players section.
func TestPlayerSettings(t *testing.T) {
	f, _ := readSectioned(t)
	if v := f.Values(); v["afk_kick_minutes"] != 5.0 || v["allow_object_scaling"] != true || v["sync_effects"] != true {
		t.Errorf("read %v %v %v", v["afk_kick_minutes"], v["allow_object_scaling"], v["sync_effects"])
	}
	p, err := Diff(f, map[string]any{"afk_kick_minutes": 15.0, "allow_object_scaling": false, "sync_effects": false}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"object-scaling off", "effects off", "afk-kick 15"}; !slices.Equal(p.Commands, want) || len(p.Restart) != 0 {
		t.Fatalf("planned %+v, want %q", p, want)
	}
	f.ApplyOffline(p.Changed)
	for path, want := range map[string]string{"players.afk_kick_minutes": `15`, "players.allow_object_scaling": `false`,
		"players.sync_effects": `false`} {
		got, ok := f.at(path)
		if b, _ := json.Marshal(got); !ok || string(b) != want {
			t.Errorf("%s = %s, want %s", path, b, want)
		}
	}
	if p, err = Diff(f, map[string]any{"afk_kick_minutes": 0.0}, nil); err != nil || !slices.Equal(p.Commands, []string{"afk-kick off"}) {
		t.Errorf("afk_kick_minutes 0 planned %+v, %v", p, err)
	}
	if _, err := Diff(f, map[string]any{"afk_kick_minutes": 1441.0}, nil); err == nil {
		t.Error("afk_kick_minutes 1441 accepted; the server allows 0 to 1440")
	}
}

// Reserved slots are gone from a sectioned server: never asked of it.
func TestSectionedRemoved(t *testing.T) {
	f, _ := readSectioned(t)
	if _, err := Diff(f, map[string]any{"reserved_slots": 2.0}, nil); err == nil {
		t.Error("reserved slots planned for a server without them")
	}
	if _, err := Diff(&File{Root: map[string]any{"max_players": json.Number("32")}}, map[string]any{"reserved_slots": 2.0}, nil); err != nil {
		t.Errorf("an older server: %v", err)
	}
}

// Bans are read from data/bans.json, along with any the config still holds
// (the server moves those there), and written back there.
func TestBansFile(t *testing.T) {
	f, path := readSectioned(t)
	bansPath := filepath.Join(filepath.Dir(path), BansFile)
	if err := os.MkdirAll(filepath.Dir(bansPath), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(bansPath, []byte(`[{"id": "76561198000000010", "name": "A", "added": 5}]`), 0o600)
	f.Root["bans"] = []any{map[string]any{"id": "76561198000000011", "name": "B"}}
	f.Write(path)

	f, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if b := f.Bans(); len(b) != 2 || b[0].ID != "76561198000000010" || b[0].Added != 5 || b[1].Name != "B" {
		t.Fatalf("bans %+v", b)
	}
	f.SetBans(f.Bans()[1:])
	if err := f.Write(path); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(bansPath)
	cfg, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "76561198000000011") || strings.Contains(string(data), "76561198000000010") ||
		strings.Contains(string(cfg), `"bans"`) {
		t.Errorf("bans.json %s\nconfig %s", data, cfg)
	}

	// A bans file that can't be read stops the server, so it isn't written over.
	os.WriteFile(bansPath, []byte(`{"id": 1}`), 0o600)
	if _, err := Read(path); err == nil {
		t.Error("a broken bans file read")
	}

	// A flat config with no bans file keeps its bans in itself.
	flat := filepath.Join(t.TempDir(), "ReSkateServer.json")
	os.WriteFile(flat, []byte(`{"name": "Flat", "bans": []}`), 0o600)
	g, err := Read(flat)
	if err != nil {
		t.Fatal(err)
	}
	g.SetBans([]Ban{{ID: "76561198000000012"}})
	g.Write(flat)
	if _, err := os.Stat(filepath.Join(filepath.Dir(flat), BansFile)); err == nil {
		t.Error("a flat config's bans went to data/bans.json")
	}
	if g, _ = Read(flat); len(g.Bans()) != 1 {
		t.Errorf("flat bans %+v", g.Bans())
	}
}
