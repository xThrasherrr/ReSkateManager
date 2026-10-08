package serverconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadMods(t *testing.T) {
	dir := t.TempDir()
	if mods, err := ReadMods(dir); err != nil || len(mods) != 0 {
		t.Fatalf("no Mods folder: %v, %v", mods, err)
	}
	write := func(mod, file, text string) {
		os.MkdirAll(filepath.Join(dir, "Mods", mod), 0o755)
		os.WriteFile(filepath.Join(dir, "Mods", mod, file), []byte(text), 0o644)
	}
	write("bbcity", "reskate-levels.json", "\xEF\xBB\xBF"+`{"levels":[
		{"asset":"Levels/Custom/BBCity/BBCity_LevelRoot","displayName":"BB City"},
		{"asset":"Levels/Custom/dingolevel_Night_Park"},
		{"asset":"levels\\game\\bam_levelroot\\bam_levelroot"}]}`)
	write("bbcity", "manifest.json", `{"name":"Big Board City","author":"sk8r","version_number":"1.2.0"}`)
	write("broken", "reskate-levels.json", `{"levels":[`)
	write("textures", "layout.toc", "")
	os.WriteFile(filepath.Join(dir, "Mods", "mods.json"), []byte(`{}`), 0o644)

	mods, err := ReadMods(dir)
	if err != nil || len(mods) != 3 {
		t.Fatalf("%d mods, %v", len(mods), err)
	}
	bb := mods[0]
	if bb.Folder != "bbcity" || bb.Title != "Big Board City" || bb.Author != "sk8r" || bb.Version != "1.2.0" || bb.Problem != "" {
		t.Errorf("bbcity: %+v", bb)
	}
	want := []ModMap{
		{Name: "BB City", Asset: "Levels/Custom/BBCity/BBCity_LevelRoot"},
		{Name: "Night Park", Asset: "Levels/Custom/dingolevel_Night_Park"},
		{Name: "San Vansterdam", Asset: `levels\game\bam_levelroot\bam_levelroot`, Shadowed: true},
	}
	if len(bb.Maps) != len(want) {
		t.Fatalf("bbcity maps: %+v", bb.Maps)
	}
	for i := range want {
		if bb.Maps[i] != want[i] {
			t.Errorf("map %d: %+v, want %+v", i, bb.Maps[i], want[i])
		}
	}
	if mods[1].Folder != "broken" || mods[1].Problem == "" || len(mods[1].Maps) != 0 {
		t.Errorf("broken: %+v", mods[1])
	}
	if mods[2].Folder != "textures" || mods[2].Problem != "" || len(mods[2].Maps) != 0 {
		t.Errorf("textures: %+v", mods[2])
	}
}

func TestPackageName(t *testing.T) {
	for _, c := range []struct{ folder, author, name, want string }{
		{"AltDoug-South_Florida-1.0.3", "", "South_Florida", "AltDoug-South_Florida"},
		{"zeex64-Full_Skate_3_Map", "zeex64", "Full_Skate_3_Map", "zeex64-Full_Skate_3_Map"},
		{"Full_Skate_3_Map", "zeex64", "Full_Skate_3_Map", "zeex64-Full_Skate_3_Map"},
		{"Sk8r-BBCity-2.0.0", "", "", "Sk8r-BBCity"},
		{"bbcity", "", "BB City", ""},
		{"bbcity", "Some Person", "BBCity", ""},
		{"Other-Thing-1.0.0", "", "BBCity", ""},
		{"my-cool-map", "", "", ""},
	} {
		if got := packageName(c.folder, c.author, c.name); got != c.want {
			t.Errorf("packageName(%q, %q, %q) = %q, want %q", c.folder, c.author, c.name, got, c.want)
		}
	}
}

func TestNewerVersion(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"1.0.4", "1.0.3", true},
		{"1.10.0", "1.9.0", true},
		{"1.0.3", "1.0.3", false},
		{"1.0", "1.0.0", false},
		{"1.0.1", "1.0", true},
		{"1.0.0", "1.2.0", false},
		{"2.0.0", "", false},
		{"beta", "1.0.0", false},
	} {
		if got := NewerVersion(c.a, c.b); got != c.want {
			t.Errorf("NewerVersion(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
