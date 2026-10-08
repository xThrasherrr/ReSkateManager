package host

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDirSize(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "a", "b"), 0o755)
	os.WriteFile(filepath.Join(dir, "one"), make([]byte, 100), 0o644)
	os.WriteFile(filepath.Join(dir, "a", "two"), make([]byte, 20), 0o644)
	os.WriteFile(filepath.Join(dir, "a", "b", "three"), make([]byte, 3), 0o644)
	other := t.TempDir()
	os.WriteFile(filepath.Join(other, "big"), make([]byte, 5000), 0o644)
	// A link to a folder isn't followed (a junction on Windows: see the
	// shared mods test in api, which makes real ones).
	if err := os.Symlink(other, filepath.Join(dir, "link")); err != nil {
		t.Log("no symlink:", err)
	}
	if n, err := DirSize(dir); n != 123 || err != nil {
		t.Errorf("size %d, %v", n, err)
	}
	if n, err := DirSize(filepath.Join(dir, "missing")); n != 0 || err != nil {
		t.Errorf("missing folder: %d, %v", n, err)
	}
}
