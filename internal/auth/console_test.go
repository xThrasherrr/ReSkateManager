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
		// The plugins release: plugin lists, and reload, off and on change
		// what runs; a plugin's own command is one the manager doesn't know.
		"plugin":                "",
		"plugin list":           "",
		"PLUGIN commands":       "",
		"plugin reload referee": SettingsEdit,
		"plugin off referee":    SettingsEdit,
		"trickrace reset":       SettingsEdit,
	} {
		if got := ConsolePerm(line); got != want {
			t.Errorf("%q needs %q, want %q", line, got, want)
		}
	}
}
