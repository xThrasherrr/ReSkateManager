package serverconfig

import (
	"archive/zip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeZip makes a zip of name → content; a name ending in "/" is a folder entry.
func writeZip(t *testing.T, files ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "upload.zip")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for i := 0; i < len(files); i += 2 {
		w, err := zw.Create(files[i])
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(files[i+1]))
	}
	zw.Close()
	f.Close()
	return p
}

const levels = `{"levels":[{"asset":"Levels/Custom/BBCity/BBCity_LevelRoot","displayName":"BB City"}]}`

func TestInstallModLayouts(t *testing.T) {
	for _, c := range []struct {
		what, zipName, want string
		files               []string
	}{
		{"files at the root, named after the zip", "Sk8r-BBCity-1.2.0.zip", "Sk8r-BBCity-1.2.0",
			[]string{"reskate-levels.json", levels, "manifest.json", `{"name":"BB City"}`}},
		{"one folder", "whatever.zip", "bbcity",
			[]string{"bbcity/", "", "bbcity/reskate-levels.json", levels, "bbcity/Levels/a.bin", "x"}},
		{"zipped from the game's Mods folder", "mods.zip", "bbcity",
			[]string{"Mods/bbcity/reskate-levels.json", levels}},
		{"Windows separators and macOS litter", "x.zip", "bbcity",
			[]string{`bbcity\reskate-levels.json`, levels, "__MACOSX/bbcity/._reskate-levels.json", "", "bbcity/.DS_Store", ""}},
	} {
		dir := t.TempDir()
		folder, replaced, err := InstallMod(dir, writeZip(t, c.files...), c.zipName)
		if err != nil || folder != c.want || replaced {
			t.Errorf("%s: %q, %v, %v; want %q", c.what, folder, replaced, err, c.want)
			continue
		}
		mods, _ := ReadMods(dir)
		if len(mods) != 1 || len(mods[0].Maps) != 1 || mods[0].Maps[0].Name != "BB City" {
			t.Errorf("%s: read back %+v", c.what, mods)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 1 {
			t.Errorf("%s: left behind %v", c.what, entries)
		}
		if fi, err := os.Stat(filepath.Join(dir, "Mods", folder)); err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o755) {
			t.Errorf("%s: mod folder %v, %v; want 0755", c.what, fi.Mode(), err)
		}
		if _, err := os.Stat(filepath.Join(dir, "Mods", folder, ".DS_Store")); err == nil {
			t.Errorf("%s: kept .DS_Store", c.what)
		}
	}
}

// ZipMaps finds the maps where installing the zip would put the mod.
func TestZipMaps(t *testing.T) {
	for _, c := range []struct {
		what  string
		files []string
		maps  int
		err   string
	}{
		{"files at the root", []string{"reskate-levels.json", levels, "manifest.json", "{}"}, 1, ""},
		{"one folder", []string{"bbcity/reskate-levels.json", levels}, 1, ""},
		{"from the game's Mods folder", []string{"Mods/bbcity/reskate-levels.json", levels}, 1, ""},
		{"a cosmetic mod", []string{"manifest.json", "{}", "Win32/deck.cas", "x"}, 0, ""},
		{"levels a level too deep", []string{"bbcity/inner/reskate-levels.json", levels, "bbcity/manifest.json", "{}"}, 0, ""},
		{"broken levels", []string{"reskate-levels.json", "{"}, 0, "not valid JSON"},
		{"several mods", []string{"Mods/a/reskate-levels.json", levels, "Mods/b/reskate-levels.json", levels}, 0, "several mods"},
		{"an unsafe path", []string{"../reskate-levels.json", levels}, 0, "unsafe path"},
	} {
		zr, err := zip.OpenReader(writeZip(t, c.files...))
		if err != nil {
			t.Fatal(err)
		}
		maps, err := ZipMaps(&zr.Reader)
		zr.Close()
		switch {
		case c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)):
			t.Errorf("%s: %v, want %q", c.what, err, c.err)
		case c.err == "" && (err != nil || maps == nil || len(maps) != c.maps):
			t.Errorf("%s: %v, %v; want %d maps", c.what, maps, err, c.maps)
		case c.maps == 1 && maps[0].Name != "BB City":
			t.Errorf("%s: %+v", c.what, maps)
		}
	}
}

func TestInstallModReplaces(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := InstallMod(dir, writeZip(t, "bbcity/old.txt", "1", "bbcity/reskate-levels.json", levels), "a.zip"); err != nil {
		t.Fatal(err)
	}
	folder, replaced, err := InstallMod(dir, writeZip(t, "bbcity/reskate-levels.json", levels), "b.zip")
	if err != nil || folder != "bbcity" || !replaced {
		t.Fatalf("%q, %v, %v", folder, replaced, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "Mods", "bbcity", "old.txt")); !os.IsNotExist(err) {
		t.Fatal("the old version's files survived the replace")
	}
}

func TestInstallModRefuses(t *testing.T) {
	type refusal struct {
		what, zipName, err string
		files              []string
	}
	cases := []refusal{
		{"a path out of the folder", "a.zip", "unsafe path", []string{"../evil.txt", "x"}},
		{"a nested path out of the folder", "a.zip", "unsafe path", []string{"bbcity/../../evil.txt", "x"}},
		{"an absolute path", "a.zip", "unsafe path", []string{"/etc/evil", "x"}},
		{"a drive letter", "a.zip", "unsafe path", []string{`C:\evil.txt`, "x"}},
		{"several mods", "a.zip", "several mods", []string{"Mods/a/x", "1", "Mods/b/x", "2"}},
		{"only folders", "a.zip", "no files", []string{"bbcity/", ""}},
		{"a bad folder name", "a<b.zip", "cannot be a mod folder", []string{"x", "1"}},
	}
	if runtime.GOOS == "windows" {
		// Windows opens a file named NUL as a device.
		cases = append(cases, refusal{"a device name", "a.zip", "unsafe path", []string{"bbcity/NUL", "x"}})
	}
	for _, c := range cases {
		dir := t.TempDir()
		_, _, err := InstallMod(dir, writeZip(t, c.files...), c.zipName)
		if err == nil || !strings.Contains(err.Error(), c.err) {
			t.Errorf("%s: %v, want %q", c.what, err, c.err)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("%s: left %v", c.what, entries)
		}
	}
	notZip := filepath.Join(t.TempDir(), "x.zip")
	os.WriteFile(notZip, []byte("hello"), 0o644)
	if _, _, err := InstallMod(t.TempDir(), notZip, "x.zip"); err == nil {
		t.Error("installed something that is not a zip")
	}
}

func TestModEnableDeleteUpdate(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := InstallMod(dir, writeZip(t, "reskate-levels.json", levels, "manifest.json", `{"name":"BBCity","version_number":"1.0.0"}`), "Sk8r-BBCity-1.0.0.zip"); err != nil {
		t.Fatal(err)
	}
	read := func() []Mod {
		mods, err := ReadMods(dir)
		if err != nil {
			t.Fatal(err)
		}
		return mods
	}
	if m := read(); len(m) != 1 || m[0].Disabled || m[0].Package != "Sk8r-BBCity" {
		t.Fatalf("installed: %+v", m)
	}

	if err := SetModEnabled(dir, "Sk8r-BBCity-1.0.0", false); err != nil {
		t.Fatal(err)
	}
	if m := read(); len(m) != 1 || !m[0].Disabled {
		t.Fatalf("disabled: %+v", m)
	}
	if _, err := os.Stat(filepath.Join(dir, "DisabledMods", "Sk8r-BBCity-1.0.0", "reskate-levels.json")); err != nil {
		t.Fatal(err)
	}

	// Re-uploading a disabled mod updates it where it is.
	if _, replaced, err := InstallMod(dir, writeZip(t, "reskate-levels.json", levels), "Sk8r-BBCity-1.0.0.zip"); err != nil || !replaced {
		t.Fatalf("re-upload: %v, %v", replaced, err)
	}
	if m := read(); len(m) != 1 || !m[0].Disabled {
		t.Fatalf("re-uploaded: %+v", m)
	}

	// An update into a newly named folder replaces the old one and stays disabled.
	folder, err := UpdateMod(dir, "Sk8r-BBCity-1.0.0", writeZip(t, "reskate-levels.json", levels, "manifest.json", `{"name":"BBCity","version_number":"1.1.0"}`), "Sk8r-BBCity-1.1.0.zip")
	if err != nil || folder != "Sk8r-BBCity-1.1.0" {
		t.Fatalf("update: %q, %v", folder, err)
	}
	if m := read(); len(m) != 1 || !m[0].Disabled || m[0].Version != "1.1.0" || m[0].Folder != folder {
		t.Fatalf("updated: %+v", m)
	}

	if err := SetModEnabled(dir, folder, true); err != nil {
		t.Fatal(err)
	}
	if m := read(); len(m) != 1 || m[0].Disabled {
		t.Fatalf("enabled: %+v", m)
	}
	if err := DeleteMod(dir, folder); err != nil {
		t.Fatal(err)
	}
	if m := read(); len(m) != 0 {
		t.Fatalf("deleted: %+v", m)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 2 { // Mods and DisabledMods
		t.Errorf("left behind %v", entries)
	}

	for _, bad := range []string{"missing", "..", "../x", `a\b`, ""} {
		if err := DeleteMod(dir, bad); err != ErrNoMod {
			t.Errorf("delete %q: %v, want ErrNoMod", bad, err)
		}
		if err := SetModEnabled(dir, bad, false); err != ErrNoMod {
			t.Errorf("disable %q: %v, want ErrNoMod", bad, err)
		}
	}
}

// A disabled mod's level does not shadow an enabled mod's copy of it.
func TestDisabledModShadowsNothing(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"Mods/b", "DisabledMods/a"} {
		os.MkdirAll(filepath.Join(dir, p), 0o755)
		os.WriteFile(filepath.Join(dir, p, "reskate-levels.json"), []byte(levels), 0o644)
	}
	mods, _ := ReadMods(dir)
	if len(mods) != 2 || mods[0].Folder != "b" || mods[0].Maps[0].Shadowed || !mods[1].Disabled || mods[1].Maps[0].Shadowed {
		t.Fatalf("%+v", mods)
	}
}

// A zip can list far more files than it has bytes for, and its list is read
// whole before anything counts it; so the list itself is capped.
func TestInstallModRefusesALongList(t *testing.T) {
	files := make([]string, 0, 4000)
	for i := range 2000 {
		files = append(files, "bbcity/f"+strings.Repeat("x", 40)+string(rune('a'+i%26))+strings.Repeat("y", i%7), "")
	}
	prev := maxModList
	maxModList = 16 << 10
	t.Cleanup(func() { maxModList = prev })
	dir := t.TempDir()
	if _, _, err := InstallMod(dir, writeZip(t, files...), "a.zip"); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("a 2000-file list in 16 KB: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("left %v", entries)
	}
}

// What the files unpack to is in the zip's list, and archive/zip holds each
// file to it, so a mod too big is refused before anything is written.
func TestInstallModRefusesTooBigUpFront(t *testing.T) {
	prev := maxModBytes
	maxModBytes = 1 << 10
	t.Cleanup(func() { maxModBytes = prev })
	dir := t.TempDir()
	zipped := writeZip(t, "bbcity/a", strings.Repeat("a", 600), "bbcity/b", strings.Repeat("b", 600))
	if _, _, err := InstallMod(dir, zipped, "a.zip"); err == nil || !strings.Contains(err.Error(), "unpacks to more than") {
		t.Fatalf("1.2 KB of files over a 1 KB limit: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("left %v", entries)
	}
}

// At the start, what installs, uploads and deletes left behind goes, except a
// mod's old copy set aside while its new one went in, the only copy left.
func TestSweepLeftovers(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{".mod-123/x", ".mod-123.old/x", ".mod-delete-9/x", "Mods/bbcity/x", "keep.txt"} {
		os.MkdirAll(filepath.Join(dir, filepath.Dir(p)), 0o755)
		os.WriteFile(filepath.Join(dir, p), nil, 0o644)
	}
	os.WriteFile(filepath.Join(dir, ".mod-upload-1.zip"), nil, 0o644)
	os.WriteFile(filepath.Join(dir, ".mod-download-2.zip"), nil, 0o644)
	kept := SweepLeftovers(dir)
	if len(kept) != 1 || filepath.Base(kept[0]) != ".mod-123.old" {
		t.Errorf("kept %v", kept)
	}
	entries, _ := os.ReadDir(dir)
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	if strings.Join(left, ",") != ".mod-123.old,Mods,keep.txt" {
		t.Errorf("left %v", left)
	}
}
