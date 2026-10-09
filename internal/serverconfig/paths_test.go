package serverconfig

import "testing"

func TestPaths(t *testing.T) {
	got := Paths()
	for place, key := range map[string]string{
		"players.allow_voice_chat":          "voice_chat",
		"network.distances.half_rate_start": "distances.half_rate_start",
		"votes.map.enabled":                 "votes.map.enabled",
		"access.admins":                     "admins",
		"server.auto_update":                "auto_update",
	} {
		if got[place] != key {
			t.Errorf("Paths()[%q] = %q, want %q", place, got[place], key)
		}
	}
	for place, key := range got {
		if key == "reserved_slots" {
			t.Errorf("Paths() has the removed reserved_slots at %q", place)
		}
	}
}
