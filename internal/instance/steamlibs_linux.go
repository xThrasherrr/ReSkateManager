package instance

import (
	"os"
	"path/filepath"
)

// linkSteamClient points ~/.steam/sdk64/steamclient.so, where libsteam_api.so
// loads Steam from, at the copy in the server folder, as the release's
// setup-linux-server-libs.sh does. Anything usable already there is left alone,
// since other servers on the host may rely on it. It reports what it linked.
func linkSteamClient(dir string) (string, error) {
	src := filepath.Join(dir, "steamclient.so")
	if _, err := os.Stat(src); err != nil {
		return "", nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dst := filepath.Join(home, ".steam", "sdk64", "steamclient.so")
	if _, err := os.Stat(dst); err == nil {
		return "", nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	_ = os.Remove(dst) // a dangling link, e.g. to a server folder that was deleted
	if err := os.Symlink(src, dst); err != nil {
		return "", err
	}
	return "Linked " + dst + " to " + src, nil
}
