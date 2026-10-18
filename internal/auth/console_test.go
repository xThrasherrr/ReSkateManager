package auth

import "testing"

// 2.1.0's tool locks are a moderator's, as a kick is, and the list of them
// only reads; what a throwdown locks and the drones are settings.
func TestConsolePermLocks(t *testing.T) {
	for line, want := range map[string]string{
		"lock bob all":             PlayersKick,
		"UNLOCK 76561198000000001": PlayersKick,
		"locks":                    "",
		"throwdown-locks off":      SettingsEdit,
		"drones on":                SettingsEdit,
	} {
		if got := ConsolePerm(line); got != want {
			t.Errorf("%q needs %q, want %q", line, got, want)
		}
	}
}
