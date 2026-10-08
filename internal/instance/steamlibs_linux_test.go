package instance

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLinkSteamClient(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dst := filepath.Join(home, ".steam", "sdk64", "steamclient.so")
	server := t.TempDir()

	// No steamclient.so in the server folder: nothing to link.
	if msg, err := linkSteamClient(server); err != nil || msg != "" {
		t.Fatalf("empty folder: %q, %v", msg, err)
	}

	src := filepath.Join(server, "steamclient.so")
	os.WriteFile(src, []byte("so"), 0o644)
	if msg, err := linkSteamClient(server); err != nil || msg == "" {
		t.Fatalf("first link: %q, %v", msg, err)
	}
	if got, _ := os.Readlink(dst); got != src {
		t.Fatalf("link points at %q", got)
	}

	// A working link, even to another server, is left alone.
	other := t.TempDir()
	os.WriteFile(filepath.Join(other, "steamclient.so"), []byte("so"), 0o644)
	if msg, _ := linkSteamClient(other); msg != "" {
		t.Fatalf("replaced a working link: %q", msg)
	}

	// A dangling link (its server folder was deleted) is replaced.
	os.Remove(src)
	if msg, err := linkSteamClient(other); err != nil || msg == "" {
		t.Fatalf("dangling link: %q, %v", msg, err)
	}
	if got, _ := os.Readlink(dst); got != filepath.Join(other, "steamclient.so") {
		t.Fatalf("link points at %q", got)
	}
}
