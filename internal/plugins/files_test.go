package plugins

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// The editor reads and writes a plugin's files, inside its folder only, and
// never over a change it didn't see.
func TestFiles(t *testing.T) {
	m := newManager(t)
	addPlugin(t, m, "hello", "", `log("up")`, "lib/util.lua", "return 1", ".hidden/x.lua", "x")

	files, err := m.Files("hello")
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	if want := []string{"lib/util.lua", "main.lua", "plugin.toml"}; !slices.Equal(paths, want) {
		t.Errorf("files: %q, want %q", paths, want)
	}
	if _, err := m.Files("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("no such plugin: %v", err)
	}

	text, hash, err := m.ReadFile("hello", "main.lua")
	if err != nil || text != `log("up")` {
		t.Fatalf("read: %q %v", text, err)
	}
	for _, bad := range []string{"", "../x.lua", "/etc/passwd", `lib\util.lua`, "C:x", "lib/../main.lua", ".hidden/x.lua", "lib/.x"} {
		if _, err := m.WriteFile("hello", bad, "x", ""); !errors.Is(err, ErrBadFile) {
			t.Errorf("wrote %q: %v", bad, err)
		}
		if _, _, err := m.ReadFile("hello", bad); !errors.Is(err, ErrBadFile) {
			t.Errorf("read %q: %v", bad, err)
		}
	}

	next, err := m.WriteFile("hello", "main.lua", `log("down")`, hash)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(m.Dir, "hello", "main.lua")); string(b) != `log("down")` {
		t.Errorf("on disk: %q", b)
	}
	// A second save from what was read first would undo the one between.
	if cur, err := m.WriteFile("hello", "main.lua", "lost", hash); !errors.Is(err, ErrChanged) || cur != next {
		t.Errorf("stale save: %q %v", cur, err)
	}
	if _, err := m.WriteFile("hello", "lib/util.lua", "x", ""); !errors.Is(err, ErrChanged) {
		t.Errorf("new over one there: %v", err)
	}
	if _, err := m.WriteFile("hello", "lib/deep/new.lua", "return 2", ""); err != nil {
		t.Errorf("new file: %v", err)
	}
	if _, err := m.WriteFile("hello", "bin.lua", "\xff\xfe", ""); !errors.Is(err, ErrBadFile) {
		t.Errorf("not text: %v", err)
	}
	if files, _ := m.Files("hello"); len(files) != 4 {
		t.Errorf("a temp file stayed, or the new one is missing: %+v", files)
	}
	if !hasLine(m, "hello", "Saved main.lua from the editor.") {
		t.Errorf("the save isn't on its card: %q", lines(m, "hello"))
	}
}
