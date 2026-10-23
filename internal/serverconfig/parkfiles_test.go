package serverconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testPark = `{"schema_version": 1, "revision": 3, "format": "reskate-park", "maps": {"bam": [
  {"id": 1, "item": "own_bk_rail", "position": [10, 0, -5], "rotation": [0, 0, 0, 1]},
  {"id": 2, "item": "own_bk_box", "position": [-2, 1.5, 20], "rotation": [0, 0, 0, 1]}]}}`

// A park is read as the server's load_park_mod reads it: refused whole only
// when it isn't a park or nothing in it can be shown, its other broken
// objects skipped.
func TestCheckPark(t *testing.T) {
	n, skipped, box, err := checkPark([]byte(testPark), "bam")
	if err != nil || n != 2 || skipped != 0 || box != [6]float64{-2, 0, -5, 10, 1.5, 20} {
		t.Fatalf("%d objects, %d skipped in %v, %v", n, skipped, box, err)
	}
	mixed := `{"format": "reskate-park", "maps": {"grom": [
		{"id": 1, "item": "own_bk_pad", "position": [0, 10, 0], "rotation": [0, 0, 0, 1]},
		{"id": 2, "item": "vehicle_car", "position": [1, 10, 0], "rotation": [0, 0, 0, 1]},
		{"id": 3, "item": "own_bk_pad", "position": [2, 10, 0], "rotation": [0, 0, 0, 0]},
		{"id": 4, "item": "own_bk_pad", "position": [3, 10]},
		{"id": 5, "item": "own_bk_pad", "position": [4, 10, 0], "rotation": [0, 0, 0, 1], "extra": true},
		{"id": 6, "item": "own_bk_pad", "position": [5, 10, 0], "rotation": [0, 0, 0, 1]}]},
		"object_scales": {"grom": {"5": 2.5, "6": 500}}}`
	if n, skipped, _, err := checkPark([]byte(mixed), "grom"); err != nil || n != 2 || skipped != 4 {
		t.Errorf("mixed: %d objects, %d skipped, %v", n, skipped, err)
	}
	for _, c := range []struct{ name, park, key, want string }{
		{"not JSON", `{"maps":`, "bam", "isn't valid JSON"},
		{"no format", strings.Replace(testPark, `"format": "reskate-park", `, ``, 1), "bam", "isn't a ReSkate park"},
		{"newer", strings.Replace(testPark, `"schema_version": 1`, `"schema_version": 2`, 1), "bam", "newer ReSkate"},
		{"another key", testPark, "grom", "no objects for grom"},
		{"empty", `{"format": "reskate-park", "maps": {"bam": []}}`, "bam", "has no objects"},
		{"none shown", strings.ReplaceAll(testPark, `"own_bk_`, `"car_`), "bam", "Build Kit"},
	} {
		if _, _, _, err := checkPark([]byte(c.park), c.key); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}

// The park editor reads and writes a mod's manifest and parks, checked as
// the server reads them, and nothing else: not another path, not a shared mod.
func TestModFiles(t *testing.T) {
	dir := t.TempDir()
	mod := filepath.Join(ModsDir(dir), "street park")
	if err := os.MkdirAll(filepath.Join(mod, "parks"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(mod, "manifest.json"), []byte(`{"name":"Street"}`), 0o644)
	os.WriteFile(filepath.Join(mod, "parks", "bam.park.json"), []byte(testPark), 0o644)
	os.WriteFile(filepath.Join(mod, "parks", "grom.park.json"), []byte(`{}`), 0o644)

	parks, err := ReadParks(dir)
	if err != nil || len(parks) != 2 || parks[0].Key != "bam" || parks[0].Objects != 2 || parks[0].Problem != "" || parks[1].Problem == "" {
		t.Fatalf("%+v, %v", parks, err)
	}
	text, hash, err := ReadModFile(dir, "street park", "parks/bam.park.json")
	if err != nil || text != testPark {
		t.Fatalf("read %q, %v", text, err)
	}
	moved := strings.Replace(testPark, `[10, 0, -5]`, `[12, 0, -5]`, 1)
	if _, err := WriteModFile(dir, "street park", "parks/bam.park.json", moved, "stale"); !errors.Is(err, ErrModFileChanged) {
		t.Errorf("a stale save: %v", err)
	}
	if _, err := WriteModFile(dir, "street park", "parks/bam.park.json", strings.Replace(testPark, `"bam"`, `"grom"`, 1), hash); !errors.Is(err, ErrBadModFile) {
		t.Errorf("a park the server would refuse: %v", err)
	}
	if _, err := WriteModFile(dir, "street park", "parks/bam.park.json", moved, hash); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(mod, "parks", "bam.park.json")); string(b) != moved {
		t.Errorf("saved %q", b)
	}
	if _, err := WriteModFile(dir, "street park", "manifest.json", `[1]`, ""); !errors.Is(err, ErrBadModFile) {
		t.Errorf("a manifest that isn't an object: %v", err)
	}
	for _, rel := range []string{"reskate-levels.json", "../street park/manifest.json", "parks/../manifest.json", "parks/bam.park.json/x", "parks/.park.json"} {
		if _, _, err := ReadModFile(dir, "street park", rel); !errors.Is(err, ErrBadModFile) {
			t.Errorf("read %s: %v", rel, err)
		}
	}
	if _, _, err := ReadModFile(dir, "..", "manifest.json"); !errors.Is(err, ErrNoMod) {
		t.Errorf("a folder outside Mods: %v", err)
	}

	shared := filepath.Join(t.TempDir(), SharedName)
	writeMod(t, filepath.Join(ModsDir(shared), "linked"), "1.0.0")
	if err := SyncShared(dir, shared, SharedAll); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadModFile(dir, "linked", "manifest.json"); !errors.Is(err, ErrBadModFile) {
		t.Errorf("a shared mod: %v", err)
	}
}
