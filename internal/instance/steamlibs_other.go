//go:build !linux

package instance

// linkSteamClient is Linux only: on Windows the Steam DLLs load from the server folder.
func linkSteamClient(string) (string, error) { return "", nil }
