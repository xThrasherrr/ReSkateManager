package serverconfig

import "testing"

func TestPaths(t *testing.T) {
	got := Paths()
	for place, key := range map[string]string{
		"players.allow_voice_chat":          "voice_chat",
		"network.distances.half_rate_start": "distances.half_rate_start",
		"votes.map.enabled":                 "votes.map.enabled",
		"access.reserved_players_slots":     "reserved",
		"server.auto_update":                "auto_update",
	} {
		if got[place] != key {
			t.Errorf("Paths()[%q] = %q, want %q", place, got[place], key)
		}
	}
	for place, key := range got {
		if key == "reserved_slots" || key == "admins" { // the admins went into 2.0.6's roles
			t.Errorf("Paths() has the removed %s at %q", key, place)
		}
	}
}
