package plugins

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// zipOf is a zip holding files, name to contents, in the order given.
func zipOf(t *testing.T, files ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := 0; i+1 < len(files); i += 2 {
		w, err := zw.Create(files[i])
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(files[i+1]))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const helloToml = "name = \"Hello\"\nversion = \"1.0.0\"\napi = 0\n"

// A zip of a plugin's folder, or of the files in it, adds the plugin, off.
func TestInstall(t *testing.T) {
	m := newManager(t)
	st, replaced, err := m.Install(zipOf(t, "plugin.toml", helloToml, "main.lua", `log(require("lib.util").hi)`, "lib/util.lua", `return {hi = "hi"}`), "hello.zip", false)
	if err != nil || replaced || st.ID != "hello" || st.Name != "Hello" || st.State != Off || st.Enabled {
		t.Fatalf("install: %+v %v %v", st, replaced, err)
	}
	if b, err := os.ReadFile(filepath.Join(m.Dir, "hello", "lib", "util.lua")); err != nil || string(b) != `return {hi = "hi"}` {
		t.Fatalf("lib/util.lua: %q %v", b, err)
	}
	enable(t, m, "hello")
	if !hasLine(m, "hello", "Added version 1.0.0 from an upload.") || !hasLine(m, "hello", "hi") {
		t.Errorf("lines %q", lines(m, "hello"))
	}
	// Zipped as a folder, the folder names it, whatever the zip is called.
	st, _, err = m.Install(zipOf(t, "greet/plugin.toml", helloToml, "greet/main.lua", "log(1)", "__MACOSX/greet/._main.lua", "junk", "greet/.DS_Store", "junk"), "greet-1.0.0 (2).zip", false)
	if err != nil || st.ID != "greet" {
		t.Fatalf("a folder: %+v %v", st, err)
	}
	if _, err := os.Stat(filepath.Join(m.Dir, "greet", ".DS_Store")); err == nil {
		t.Error("a Mac's .DS_Store was unpacked")
	}
	if left, _ := filepath.Glob(filepath.Join(m.Dir, ".*")); len(left) != 0 {
		t.Errorf("left behind: %q", left)
	}
}

// A zip that isn't a plugin, or reaches outside its folder, is refused and
// leaves nothing.
func TestInstallRefuses(t *testing.T) {
	m := newManager(t)
	symlink := func() []byte {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for _, f := range []struct{ name, body string }{{"plugin.toml", helloToml}, {"main.lua", "log(1)"}} {
			w, _ := zw.Create(f.name)
			w.Write([]byte(f.body))
		}
		h := &zip.FileHeader{Name: "lib.lua"}
		h.SetMode(os.ModeSymlink | 0o777)
		w, _ := zw.CreateHeader(h)
		w.Write([]byte("../../manager.toml"))
		zw.Close()
		return buf.Bytes()
	}()
	many := []string{"plugin.toml", helloToml, "main.lua", "log(1)"}
	for i := range maxFiles {
		many = append(many, "f/"+strings.Repeat("x", 3)+string(rune('a'+i%26))+"/"+strings.Repeat("y", i/26+1)+".lua", "")
	}
	for _, c := range []struct {
		what, name string
		zip        []byte
		want       string
	}{
		{"not a zip", "x.zip", []byte("hello"), "it isn't a zip that can be read"},
		{"no main.lua", "x.zip", zipOf(t, "plugin.toml", helloToml), "it has no main.lua"},
		{"no plugin.toml", "x.zip", zipOf(t, "main.lua", "log(1)"), "it has no plugin.toml"},
		{"a bad plugin.toml", "x.zip", zipOf(t, "plugin.toml", "name = 1", "main.lua", "log(1)"), "Its plugin.toml has a mistake"},
		{"a future API", "x.zip", zipOf(t, "plugin.toml", "api = 1", "main.lua", "log(1)"), "It was made for plugin API 1; this manager runs API 0."},
		{"a bad name", "my plugin.zip", zipOf(t, "plugin.toml", helloToml, "main.lua", "log(1)"), `its name "my plugin" can't name a plugin`},
		{"up a folder", "x.zip", zipOf(t, "plugin.toml", helloToml, "main.lua", "log(1)", "../evil.lua", "x"), `outside the plugin's folder`},
		{"from the root", "x.zip", zipOf(t, "plugin.toml", helloToml, "main.lua", "log(1)", "/etc/evil.lua", "x"), `outside the plugin's folder`},
		{"a backslash", "x.zip", zipOf(t, "plugin.toml", helloToml, "main.lua", "log(1)", `lib\..\..\evil.lua`, "x"), `outside the plugin's folder`},
		{"a drive", "x.zip", zipOf(t, "plugin.toml", helloToml, "main.lua", "log(1)", "C:/evil.lua", "x"), `outside the plugin's folder`},
		{"a symlink", "x.zip", symlink, "lib.lua isn't a plain file"},
		{"twice", "x.zip", zipOf(t, "plugin.toml", helloToml, "main.lua", "log(1)", "main.lua", "log(2)"), "it holds main.lua twice"},
		{"a big file", "x.zip", zipOf(t, "plugin.toml", helloToml, "main.lua", strings.Repeat("-", maxSource+1)), "main.lua is over 1 MB"},
		{"many files", "x.zip", zipOf(t, many...), "a plugin may have 256"},
		{"a big zip", "x.zip", make([]byte, MaxUpload+1), "can be 4 MB at most"},
	} {
		_, _, err := m.Install(c.zip, c.name, false)
		if !errors.Is(err, ErrBadUpload) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.what, err, c.want)
		}
	}
	if entries, _ := os.ReadDir(m.Dir); len(entries) != 0 {
		t.Errorf("left in the folder: %v", entries)
	}
}

// A plugin there already is replaced only when asked, and one that was on
// runs its new files. Its grants stay; what it newly asks for waits.
func TestInstallReplaces(t *testing.T) {
	m := newManager(t)
	addPlugin(t, m, "hello", helloToml+`permissions = ["players.chat"]`, `log("one")`, "lib/old.lua", "return 1")
	grant(t, m, "hello", "players.chat")
	enable(t, m, "hello")
	two := zipOf(t, "plugin.toml", strings.Replace(helloToml, "1.0.0", "2.0.0", 1)+`permissions = ["players.chat", "players.kick"]`, "main.lua", `log("two")`)
	var exists *ExistsError
	if _, _, err := m.Install(two, "HELLO.zip", false); !errors.As(err, &exists) || exists.Plugin.ID != "hello" || err.Error() != "Hello 1.0.0 is installed already, as hello" {
		t.Fatalf("without replace: %v", err)
	}
	st, replaced, err := m.Install(two, "HELLO.zip", true)
	if err != nil || !replaced || st.ID != "hello" || st.Version != "2.0.0" {
		t.Fatalf("replace: %+v %v %v", st, replaced, err)
	}
	waitFor(t, func() bool { return hasLine(m, "hello", "two") })
	s, _ := m.Get("hello")
	if s.State != Running || !s.Enabled || !slices.Equal(s.Granted, []string{"players.chat"}) || !hasLine(m, "hello", "Replaced with version 2.0.0 from an upload.") {
		t.Errorf("after: %s, on %v, granted %q, %q", s.State, s.Enabled, s.Granted, lines(m, "hello"))
	}
	if _, err := os.Stat(filepath.Join(m.Dir, "hello", "lib", "old.lua")); err == nil {
		t.Error("the old version's file is still there")
	}
	if left, _ := filepath.Glob(filepath.Join(m.Dir, ".*")); len(left) != 0 {
		t.Errorf("left behind: %q", left)
	}
}

// Removing a plugin stops it and forgets it, its grants and that it was on;
// a reinstall starts off with nothing granted.
func TestRemove(t *testing.T) {
	m := newManager(t)
	addPlugin(t, m, "hello", helloToml+`permissions = ["players.chat"]`, `log("up")`)
	grant(t, m, "hello", "players.chat")
	enable(t, m, "hello")
	if err := m.Remove(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(m.Dir, "hello")); !os.IsNotExist(err) {
		t.Errorf("its folder: %v", err)
	}
	if _, err := m.Get("hello"); !errors.Is(err, ErrNotFound) {
		t.Errorf("still listed: %v", err)
	}
	if err := m.Remove(context.Background(), "hello"); !errors.Is(err, ErrNotFound) {
		t.Errorf("removed twice: %v", err)
	}
	st, _, err := m.Install(zipOf(t, "plugin.toml", helloToml+`permissions = ["players.chat"]`, "main.lua", "log(1)"), "hello.zip", false)
	if err != nil || st.Enabled || len(st.Granted) != 0 {
		t.Errorf("reinstalled: %+v %v", st, err)
	}
	// What a crash left mid-upload goes when the manager starts.
	os.MkdirAll(filepath.Join(m.Dir, ".upload-abc", "x"), 0o755)
	again := &Manager{Dir: m.Dir, Store: m.Store, Log: m.Log, LogPath: m.LogPath}
	if err := again.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	again.Close()
	if _, err := os.Stat(filepath.Join(m.Dir, ".upload-abc")); !os.IsNotExist(err) {
		t.Errorf("a cut-off upload stayed: %v", err)
	}
}
