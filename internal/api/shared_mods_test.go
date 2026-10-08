package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/xThrasherrr/ReSkateManager/internal/thunderstore"
)

// zipOf zips name → content pairs.
func zipOf(files ...string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := 0; i < len(files); i += 2 {
		w, _ := zw.Create(files[i])
		w.Write([]byte(files[i+1]))
	}
	zw.Close()
	return buf.Bytes()
}

const bbLevels = `{"levels":[{"asset":"Levels/Custom/BBCity/BBCity_LevelRoot"}]}`

func bbManifest(version string) string {
	return `{"name":"BBCity","author":"sk8r","version_number":"` + version + `"}`
}

// writeOwnMod puts a server's own copy of a mod at path.
func writeOwnMod(t *testing.T, path, version string) {
	t.Helper()
	os.MkdirAll(path, 0o755)
	os.WriteFile(filepath.Join(path, "reskate-levels.json"), []byte(bbLevels), 0o644)
	os.WriteFile(filepath.Join(path, "manifest.json"), []byte(bbManifest(version)), 0o644)
}

// isLink reports whether path is a symlink or (on Windows) a junction.
func isLink(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// The shared mods reach the servers that use them as links. Changing them
// takes settings.edit on every server; turning them on for a server takes it
// on that server.
func TestSharedMods(t *testing.T) {
	shared := filepath.Join(t.TempDir(), "shared")
	p, dirs := newServersPanel(t, nil, shared, "a", "b", "c")
	p.createUser("a-admin", fmt.Sprintf(`{"roleId":%d,"instance":"a"}`, p.roleID("Administrator")))
	// b keeps its own copy of the version about to be shared.
	writeOwnMod(t, filepath.Join(dirs["b"], "Mods", "bbcity"), "1.0.0")

	if code, b := p.do("PATCH", "/api/instances/a/shared-mods", `{"use":"all"}`); code != 200 {
		t.Fatalf("turn on for a: %d %s", code, b)
	}
	if code, b := p.uploadTo("/api/shared-mods/uploads", "bbcity.zip", zipOf("bbcity/manifest.json", bbManifest("1.0.0"), "bbcity/reskate-levels.json", bbLevels), 100); p.runJob(code, []byte(b), nil).Phase != "done" {
		t.Fatalf("upload: %d %s", code, b)
	}
	lib := filepath.Join(shared, "Mods", "bbcity")
	if !exists(filepath.Join(lib, "reskate-levels.json")) {
		t.Fatal("not in the shared mods")
	}
	if !isLink(filepath.Join(dirs["a"], "Mods", "bbcity")) || !exists(filepath.Join(dirs["a"], "Mods", "bbcity", "reskate-levels.json")) {
		t.Fatal("not linked into a")
	}
	if isLink(filepath.Join(dirs["b"], "Mods", "bbcity")) || exists(filepath.Join(dirs["c"], "Mods")) {
		t.Fatal("reached a server that does not use the shared mods")
	}

	// Turning them on swaps b's copy of the same version for a link.
	code, b := p.do("PATCH", "/api/instances/b/shared-mods", `{"use":"all"}`)
	if code != 200 || !isLink(filepath.Join(dirs["b"], "Mods", "bbcity")) {
		t.Fatalf("turn on for b: %d %s", code, b)
	}

	var list struct {
		Mods []struct {
			Folder string
			Shared bool
		}
		Shared *struct{ Use string }
	}
	_, b = p.do("GET", "/api/instances/a/mods", "")
	json.Unmarshal(b, &list)
	if len(list.Mods) != 1 || !list.Mods[0].Shared || list.Shared == nil || list.Shared.Use != "all" {
		t.Fatalf("a's mods: %s", b)
	}

	// From one server, a shared mod can be disabled but not deleted.
	if code, b := p.do("DELETE", "/api/instances/a/mods/bbcity", ""); code != 400 {
		t.Errorf("delete a shared mod from a server: %d %s, want 400", code, b)
	}
	if code, b := p.do("PATCH", "/api/instances/a/mods/bbcity", `{"enabled":false}`); code != 200 || !isLink(filepath.Join(dirs["a"], "DisabledMods", "bbcity")) {
		t.Fatalf("disable on a: %d %s", code, b)
	}
	if !exists(filepath.Join(lib, "reskate-levels.json")) || !isLink(filepath.Join(dirs["b"], "Mods", "bbcity")) {
		t.Fatal("disabling on a reached the shared copy or b")
	}

	// A viewer sees the shared mods but can't change them.
	p.signIn("viewer")
	if code, b := p.do("GET", "/api/shared-mods", ""); code != 200 {
		t.Errorf("viewer list: %d %s", code, b)
	}
	for _, c := range [][2]string{{"POST", "/api/shared-mods/uploads"}, {"DELETE", "/api/shared-mods/bbcity"}, {"POST", "/api/shared-mods/bbcity/update"}, {"PATCH", "/api/instances/a/shared-mods"}} {
		if code, b := p.do(c[0], c[1], `{"name":"x.zip","size":1,"enabled":false}`); code != 403 {
			t.Errorf("viewer %s %s: %d %s, want 403", c[0], c[1], code, b)
		}
	}

	// An admin of one server may turn the shared mods off there, but not
	// change what every server gets.
	p.signIn("a-admin")
	_, b = p.do("GET", "/api/shared-mods", "")
	var lib2 struct{ Servers []struct{ ID string } }
	json.Unmarshal(b, &lib2)
	if len(lib2.Servers) != 1 || lib2.Servers[0].ID != "a" {
		t.Errorf("a-admin sees servers %s", b)
	}
	for _, c := range [][2]string{{"POST", "/api/shared-mods/uploads"}, {"DELETE", "/api/shared-mods/bbcity"}} {
		if code, b := p.do(c[0], c[1], `{"name":"x.zip","size":1}`); code != 403 {
			t.Errorf("a-admin %s %s: %d %s, want 403", c[0], c[1], code, b)
		}
	}
	if code, b := p.do("PATCH", "/api/instances/b/shared-mods", `{"use":"off"}`); code != 404 {
		t.Errorf("a-admin turns off for b, which it can't see: %d %s, want 404", code, b)
	}
	if code, b := p.do("PATCH", "/api/instances/a/shared-mods", `{"use":"off"}`); code != 200 {
		t.Fatalf("a-admin turns off for a: %d %s", code, b)
	}
	if exists(filepath.Join(dirs["a"], "DisabledMods", "bbcity")) || !exists(filepath.Join(lib, "reskate-levels.json")) {
		t.Fatal("turning off left a's link or took the shared copy")
	}

	// Purging a server takes its links, not the shared mods.
	p.signIn("owner")
	if code, b := p.do("DELETE", "/api/instances/b?purge=1", ""); code != 200 {
		t.Fatalf("purge b: %d %s", code, b)
	}
	if exists(dirs["b"]) || !exists(filepath.Join(lib, "reskate-levels.json")) {
		t.Fatal("purging b went wrong")
	}

	// A server's folder can't be inside the shared mods.
	if code, b := p.do("POST", "/api/instances", fmt.Sprintf(`{"name":"inside","dir":%q}`, filepath.Join(shared, "Mods", "x"))); code != 400 {
		t.Errorf("server inside the shared mods: %d %s, want 400", code, b)
	}

	if code, b := p.do("DELETE", "/api/shared-mods/bbcity", ""); code != 200 || exists(lib) {
		t.Fatalf("delete: %d %s", code, b)
	}
}

// Moving a server's mod to the shared mods links it back and swaps other
// servers' copies of the same version; a different version stays.
func TestShareMod(t *testing.T) {
	shared := filepath.Join(t.TempDir(), "shared")
	p, dirs := newServersPanel(t, nil, shared, "a", "b", "c")
	p.createUser("a-admin", fmt.Sprintf(`{"roleId":%d,"instance":"a"}`, p.roleID("Administrator")))
	writeOwnMod(t, filepath.Join(dirs["a"], "Mods", "bbcity"), "1.0.0")
	writeOwnMod(t, filepath.Join(dirs["b"], "Mods", "bbcity"), "1.0.0")
	writeOwnMod(t, filepath.Join(dirs["c"], "Mods", "bbcity"), "1.1.0")

	if code, b := p.do("POST", "/api/instances/a/mods/bbcity/share", ""); code != 400 {
		t.Errorf("share from a server not using the shared mods: %d %s, want 400", code, b)
	}
	for _, id := range []string{"a", "b", "c"} {
		p.do("PATCH", "/api/instances/"+id+"/shared-mods", `{"use":"all"}`)
	}
	p.signIn("a-admin")
	if code, b := p.do("POST", "/api/instances/a/mods/bbcity/share", ""); code != 403 {
		t.Errorf("a-admin shares: %d %s, want 403", code, b)
	}

	p.signIn("owner")
	code, b := p.do("POST", "/api/instances/a/mods/bbcity/share", "")
	var res struct{ Swapped []string }
	json.Unmarshal(b, &res)
	if code != 200 || !slices.Equal(res.Swapped, []string{"b"}) {
		t.Fatalf("share: %d %s", code, b)
	}
	if !exists(filepath.Join(shared, "Mods", "bbcity", "manifest.json")) {
		t.Fatal("not moved to the shared mods")
	}
	for id, link := range map[string]bool{"a": true, "b": true, "c": false} {
		if isLink(filepath.Join(dirs[id], "Mods", "bbcity")) != link {
			t.Errorf("%s: linked %v, want %v", id, !link, link)
		}
	}
	// c keeps its own copy, and its mods page says a shared one waits.
	_, b = p.do("GET", "/api/instances/c/mods", "")
	var list struct{ Shared struct{ Own []string } }
	json.Unmarshal(b, &list)
	if !slices.Equal(list.Shared.Own, []string{"bbcity"}) {
		t.Errorf("c's mods: %s", b)
	}
	// Deleting it brings in the shared one.
	if code, b := p.do("DELETE", "/api/instances/c/mods/bbcity", ""); code != 200 || !isLink(filepath.Join(dirs["c"], "Mods", "bbcity")) {
		t.Errorf("delete c's copy: %d %s", code, b)
	}
}

// Updating a shared mod from Thunderstore moves each server's link to the new
// folder, disabled where it was.
func TestUpdateSharedMod(t *testing.T) {
	zipped := zipOf("manifest.json", `{"name":"BBCity","version_number":"1.1.0"}`, "reskate-levels.json", bbLevels)
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveListing(w, r, ts.URL, `[{"full_name":"Sk8r-BBCity","package_url":"`+ts.URL+`/p/","versions":[
			{"version_number":"1.1.0","download_url":"`+ts.URL+`/package/download/Sk8r/BBCity/1.1.0/"}]}]`) {
			return
		}
		switch r.URL.Path {
		case "/package/download/Sk8r/BBCity/1.1.0/":
			w.Write(zipped)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)
	client := thunderstore.New("reskate")
	client.Base = ts.URL

	shared := filepath.Join(t.TempDir(), "shared")
	p, dirs := newServersPanel(t, client, shared, "a", "b")
	os.MkdirAll(filepath.Join(shared, "Mods", "Sk8r-BBCity-1.0.0"), 0o755)
	os.WriteFile(filepath.Join(shared, "Mods", "Sk8r-BBCity-1.0.0", "manifest.json"), []byte(`{"name":"BBCity","version_number":"1.0.0"}`), 0o644)
	for _, id := range []string{"a", "b"} {
		p.do("PATCH", "/api/instances/"+id+"/shared-mods", `{"use":"all"}`)
	}
	p.do("PATCH", "/api/instances/a/mods/Sk8r-BBCity-1.0.0", `{"enabled":false}`)

	if code, b := p.do("POST", "/api/instances/b/mods/Sk8r-BBCity-1.0.0/update", ""); code != 400 {
		t.Errorf("update a shared mod from a server: %d %s, want 400", code, b)
	}
	code, b := p.do("GET", "/api/shared-mods/updates", "")
	var updates map[string]modUpdate
	json.Unmarshal(b, &updates)
	if code != 200 || !updates["Sk8r-BBCity-1.0.0"].Newer {
		t.Fatalf("updates: %d %s", code, b)
	}
	id := p.startJob("/api/shared-mods/Sk8r-BBCity-1.0.0/update", "")
	if st := p.waitJob("/api/shared-mods/installs/" + id); st.Phase != "done" || st.Folder != "Sk8r-BBCity-1.1.0" {
		t.Fatalf("update: %+v", st)
	}
	if !exists(filepath.Join(shared, "Mods", "Sk8r-BBCity-1.1.0", "reskate-levels.json")) || exists(filepath.Join(shared, "Mods", "Sk8r-BBCity-1.0.0")) {
		t.Fatal("the shared copy was not updated")
	}
	if !isLink(filepath.Join(dirs["a"], "DisabledMods", "Sk8r-BBCity-1.1.0")) || exists(filepath.Join(dirs["a"], "DisabledMods", "Sk8r-BBCity-1.0.0")) || exists(filepath.Join(dirs["a"], "Mods", "Sk8r-BBCity-1.1.0")) {
		t.Error("a's disabled link did not follow the update")
	}
	if !isLink(filepath.Join(dirs["b"], "Mods", "Sk8r-BBCity-1.1.0")) || exists(filepath.Join(dirs["b"], "Mods", "Sk8r-BBCity-1.0.0")) {
		t.Error("b's link did not follow the update")
	}
}

// A server that picks its shared mods gets each mod shared later disabled; it
// loads only the ones turned on there. Switching between all and pick leaves
// each mod as it was.
func TestPickSharedMods(t *testing.T) {
	shared := filepath.Join(t.TempDir(), "shared")
	p, dirs := newServersPanel(t, nil, shared, "lobby", "mapday")
	writeOwnMod(t, filepath.Join(shared, "Mods", "bbcity"), "1.0.0")
	p.do("PATCH", "/api/instances/lobby/shared-mods", `{"use":"all"}`)
	if code, b := p.do("PATCH", "/api/instances/mapday/shared-mods", `{"use":"pick"}`); code != 200 {
		t.Fatalf("pick: %d %s", code, b)
	}
	if code, b := p.do("PATCH", "/api/instances/mapday/shared-mods", `{"use":"some"}`); code != 400 {
		t.Errorf("unknown use: %d %s, want 400", code, b)
	}
	if !isLink(filepath.Join(dirs["lobby"], "Mods", "bbcity")) || !isLink(filepath.Join(dirs["mapday"], "DisabledMods", "bbcity")) {
		t.Fatal("bbcity should load on lobby and wait on mapday")
	}

	// mapday turns on the one map it runs; a mod shared later stays off there.
	if code, b := p.do("PATCH", "/api/instances/mapday/mods/bbcity", `{"enabled":true}`); code != 200 {
		t.Fatalf("enable on mapday: %d %s", code, b)
	}
	if code, b := p.uploadTo("/api/shared-mods/uploads", "park.zip", zipOf("park/reskate-levels.json", bbLevels), 100); p.runJob(code, []byte(b), nil).Phase != "done" {
		t.Fatalf("upload: %d %s", code, b)
	}
	if !isLink(filepath.Join(dirs["lobby"], "Mods", "park")) || !isLink(filepath.Join(dirs["mapday"], "DisabledMods", "park")) {
		t.Error("park should load on lobby and wait on mapday")
	}
	if !isLink(filepath.Join(dirs["mapday"], "Mods", "bbcity")) {
		t.Error("the upload undid mapday's choice")
	}

	// Switching lobby to pick keeps what it loads; the next mod waits.
	p.do("PATCH", "/api/instances/lobby/shared-mods", `{"use":"pick"}`)
	writeOwnMod(t, filepath.Join(shared, "Mods", "later"), "1.0.0")
	p.do("PATCH", "/api/instances/lobby/shared-mods", `{"use":"pick"}`) // relinks
	if !isLink(filepath.Join(dirs["lobby"], "Mods", "park")) || !isLink(filepath.Join(dirs["lobby"], "DisabledMods", "later")) {
		t.Error("switching to pick changed what lobby loads, or a later mod loaded")
	}
	// mapday links the hand-copied mod at its next start; until then the list
	// has it in neither.
	_, b := p.do("GET", "/api/shared-mods", "")
	var list struct {
		Servers []struct {
			ID                string
			Use               string
			Enabled, Disabled []string
		}
	}
	json.Unmarshal(b, &list)
	for _, s := range list.Servers {
		if s.ID == "mapday" && (s.Use != "pick" || !slices.Equal(s.Enabled, []string{"bbcity"}) || !slices.Equal(s.Disabled, []string{"park"})) {
			t.Errorf("mapday: %+v", s)
		}
	}
}
