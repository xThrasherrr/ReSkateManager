package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/thunderstore"
)

// storeMod is a version of a package the fake Thunderstore serves. A hidden
// one's zip is not beside its icon, as with a long name on Thunderstore.
type storeMod struct {
	pkg, version string
	zip          []byte
	hidden       bool
}

// fakeStore serves mods as Thunderstore does: the package listing, download
// links, and each zip beside its icon, readable by ranges. A package's
// versions go newest first. It counts the downloads.
func fakeStore(t *testing.T, mods ...storeMod) (*thunderstore.Client, *atomic.Int32) {
	t.Helper()
	var downloads atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/c/reskate/api/") || strings.HasPrefix(r.URL.Path, "/listing/") {
			var order []string
			versions := map[string][]string{}
			for _, m := range mods {
				ns, name, _ := strings.Cut(m.pkg, "-")
				full := m.pkg + "-" + m.version
				if versions[m.pkg] == nil {
					order = append(order, m.pkg)
				}
				versions[m.pkg] = append(versions[m.pkg], fmt.Sprintf(`{"full_name":%q,"description":"A park","icon":"%s/live/repository/icons/%s.png","version_number":%q,
					"dependencies":[],"download_url":"%s/package/download/%s/%s/%s/","downloads":5,"date_created":"2026-10-01T10:00:00Z","file_size":%d}`,
					full, srv.URL, full, m.version, srv.URL, ns, name, m.version, len(m.zip)))
			}
			var pkgs []string
			for _, p := range order {
				ns, name, _ := strings.Cut(p, "-")
				pkgs = append(pkgs, fmt.Sprintf(`{"name":%q,"owner":%q,"full_name":%q,"package_url":"%s/c/reskate/p/%s/%s/","date_updated":"2026-10-02T10:00:00Z",
					"rating_score":3,"categories":["Mods"],"versions":[%s]}`, name, ns, p, srv.URL, ns, name, strings.Join(versions[p], ",")))
			}
			if serveListing(w, r, srv.URL, "["+strings.Join(pkgs, ",")+"]") {
				return
			}
		}
		for _, m := range mods {
			ns, name, _ := strings.Cut(m.pkg, "-")
			switch r.URL.Path {
			case fmt.Sprintf("/package/download/%s/%s/%s/", ns, name, m.version):
				downloads.Add(1)
				w.Write(m.zip)
				return
			case "/live/repository/packages/" + m.pkg + "-" + m.version + ".zip":
				if !m.hidden {
					http.ServeContent(w, r, "mod.zip", time.Time{}, bytes.NewReader(m.zip))
					return
				}
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	c := thunderstore.New("reskate")
	c.Base = srv.URL
	return c, &downloads
}

func bbZip(version string) []byte {
	return zipOf("manifest.json", `{"name":"BBCity","version_number":"`+version+`"}`, "reskate-levels.json", bbLevels)
}

// startJob starts an install, or an update, and answers its job's id.
func (p *panel) startJob(path, body string) string {
	p.t.Helper()
	code, b := p.do("POST", path, body)
	var job struct{ ID string }
	if json.Unmarshal(b, &job); code != http.StatusAccepted || job.ID == "" {
		p.t.Fatalf("POST %s: %d %s", path, code, b)
	}
	return job.ID
}

// waitJob polls the job at path until it finishes.
func (p *panel) waitJob(path string) modJobStatus {
	p.t.Helper()
	for range 500 {
		code, b := p.do("GET", path, "")
		var st modJobStatus
		json.Unmarshal(b, &st)
		if code != 200 {
			p.t.Fatalf("GET %s: %d %s", path, code, b)
		}
		if st.Phase == "done" || st.Phase == "failed" {
			return st
		}
		time.Sleep(10 * time.Millisecond)
	}
	p.t.Fatalf("%s never finished", path)
	return modJobStatus{}
}

// install installs a package from the fake Thunderstore through the panel,
// to base (a server's mods or the shared ones), and waits for it.
func (p *panel) install(base, body string) modJobStatus {
	p.t.Helper()
	return p.waitJob(base + "/installs/" + p.startJob(base+"/installs", body))
}

// The browser lists Thunderstore's packages to anyone who can see a server's
// settings, and looks inside a version's zip for the maps it adds.
func TestBrowseMods(t *testing.T) {
	ts, downloads := fakeStore(t,
		storeMod{"Sk8r-BBCity", "1.1.0", bbZip("1.1.0"), false},
		storeMod{"Sk8r-BBCity", "1.0.0", bbZip("1.0.0"), false},
		storeMod{"Drip-Deck", "2.0.0", zipOf("manifest.json", `{"name":"Deck"}`, "Win32/deck.cas", "x"), false},
		storeMod{"Long-Name", "1.0.0", bbZip("1.0.0"), true},
	)
	p, _ := newModsPanel(t, ts)
	p.signIn("viewer")

	code, b := p.do("GET", "/api/thunderstore/packages", "")
	var list struct {
		Mods    []browseMod
		Fetched int64
	}
	json.Unmarshal(b, &list)
	if code != 200 || len(list.Mods) != 3 || list.Fetched == 0 {
		t.Fatalf("list: %d %s", code, b)
	}
	var bb *browseMod
	for i := range list.Mods {
		if list.Mods[i].FullName == "Sk8r-BBCity" {
			bb = &list.Mods[i]
		}
	}
	if bb == nil || bb.Owner != "Sk8r" || bb.Downloads != 10 || len(bb.Versions) != 2 || bb.Versions[0].Version != "1.1.0" ||
		bb.Description != "A park" || bb.Updated == 0 || bb.Maps != nil || !strings.HasSuffix(bb.Icon, "Sk8r-BBCity-1.1.0.png") {
		t.Fatalf("BBCity: %+v", bb)
	}

	var maps struct {
		Maps    []struct{ Name string }
		Problem string
	}
	code, b = p.do("GET", "/api/thunderstore/maps?package=sk8r-bbcity", "")
	if json.Unmarshal(b, &maps); code != 200 || len(maps.Maps) != 1 || maps.Problem != "" {
		t.Fatalf("BBCity's maps: %d %s", code, b)
	}
	code, b = p.do("GET", "/api/thunderstore/maps?package=Drip-Deck&version=2.0.0", "")
	if !strings.Contains(string(b), `"maps":[]`) || code != 200 {
		t.Fatalf("a deck's maps: %d %s", code, b)
	}
	code, b = p.do("GET", "/api/thunderstore/maps?package=Long-Name", "")
	if !strings.Contains(string(b), `"maps":null`) || !strings.Contains(string(b), "cannot look inside") || code != 200 {
		t.Fatalf("a zip that cannot be looked inside: %d %s", code, b)
	}
	if code, b := p.do("GET", "/api/thunderstore/maps?package=Sk8r-BBCity&version=9.9.9", ""); code != 404 {
		t.Errorf("a version that does not exist: %d %s, want 404", code, b)
	}
	if code, b := p.do("GET", "/api/thunderstore/maps?package=Nobody-Nothing", ""); code != 404 {
		t.Errorf("a package that does not exist: %d %s, want 404", code, b)
	}
	if downloads.Load() != 0 {
		t.Error("looking inside a mod counted as a download")
	}

	// The list now knows the newest version's maps.
	_, b = p.do("GET", "/api/thunderstore/packages", "")
	json.Unmarshal(b, &list)
	for _, m := range list.Mods {
		if m.FullName == "Long-Name" {
			continue
		}
		if want := map[string]int{"Sk8r-BBCity": 1, "Drip-Deck": 0}[m.FullName]; m.Maps == nil || len(m.Maps) != want {
			t.Errorf("%s: maps %v, want %d", m.FullName, m.Maps, want)
		}
	}
}

// Installing from Thunderstore needs settings.edit on the server. Another
// version of the mod is replaced, staying disabled if it was.
func TestInstallMod(t *testing.T) {
	ts, downloads := fakeStore(t,
		storeMod{"Sk8r-BBCity", "1.1.0", bbZip("1.1.0"), false},
		storeMod{"Sk8r-BBCity", "1.0.0", bbZip("1.0.0"), false},
	)
	p, dirs := newServersPanel(t, ts, "", "a", "b")
	dir := dirs["a"]

	st := p.install("/api/instances/a/mods", `{"package":"Sk8r-BBCity","version":"1.0.0"}`)
	if st.Phase != "done" || st.Folder != "Sk8r-BBCity-1.0.0" || st.Version != "1.0.0" || st.Done != st.Total || st.Total == 0 {
		t.Fatalf("install: %+v", st)
	}
	if !exists(filepath.Join(dir, "Mods", "Sk8r-BBCity-1.0.0", "reskate-levels.json")) || downloads.Load() != 1 {
		t.Fatal("not installed")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".mod-*")); len(left) > 0 {
		t.Fatalf("left behind: %v", left)
	}
	if code, b := p.do("POST", "/api/instances/a/mods/installs", `{"package":"Sk8r-BBCity","version":"1.0.0"}`); code != 409 {
		t.Errorf("the same version again: %d %s, want 409", code, b)
	}
	if code, b := p.do("POST", "/api/instances/a/mods/installs", `{"package":"Sk8r-Nothing"}`); code != 404 {
		t.Errorf("a package that does not exist: %d %s, want 404", code, b)
	}

	// The newest version takes the old one's place, disabled as it was.
	p.do("PATCH", "/api/instances/a/mods/Sk8r-BBCity-1.0.0", `{"enabled":false}`)
	id := p.startJob("/api/instances/a/mods/installs", `{"package":"Sk8r-BBCity"}`)
	if code, b := p.do("GET", "/api/instances/b/mods/installs/"+id, ""); code != 404 {
		t.Errorf("a's job read through b: %d %s, want 404", code, b)
	}
	if st := p.waitJob("/api/instances/a/mods/installs/" + id); st.Phase != "done" || st.Folder != "Sk8r-BBCity-1.1.0" {
		t.Fatalf("replace: %+v", st)
	}
	if !exists(filepath.Join(dir, "DisabledMods", "Sk8r-BBCity-1.1.0")) || exists(filepath.Join(dir, "DisabledMods", "Sk8r-BBCity-1.0.0")) {
		t.Error("the newest version did not take the old one's place")
	}

	_, b := p.do("GET", "/api/audit", "")
	if !strings.Contains(string(b), `"mod.install"`) || !strings.Contains(string(b), "Sk8r-BBCity-1.0.0 -\\u003e Sk8r-BBCity-1.1.0") {
		t.Errorf("audit: %s", b)
	}

	p.signIn("viewer")
	if code, b := p.do("POST", "/api/instances/a/mods/installs", `{"package":"Sk8r-BBCity"}`); code != 403 {
		t.Errorf("viewer install: %d %s, want 403", code, b)
	}
	if code, b := p.do("GET", "/api/instances/a/mods/installs/"+id, ""); code != 403 {
		t.Errorf("viewer reads a job: %d %s, want 403", code, b)
	}
}

// A mod added to the shared mods from Thunderstore reaches every server that
// uses them: loaded where they all load, turned off where they are picked.
// Installing that version on a server that uses them loads the shared copy,
// with nothing to download, in place of the server's own.
func TestInstallSharedMod(t *testing.T) {
	ts, downloads := fakeStore(t, storeMod{"Sk8r-BBCity", "1.1.0", bbZip("1.1.0"), false}, storeMod{"Sk8r-BBCity", "1.0.0", bbZip("1.0.0"), false})
	shared := filepath.Join(t.TempDir(), "shared")
	p, dirs := newServersPanel(t, ts, shared, "lobby", "mapday", "other", "off")
	p.do("PATCH", "/api/instances/lobby/shared-mods", `{"use":"all"}`)
	p.do("PATCH", "/api/instances/mapday/shared-mods", `{"use":"pick"}`)
	p.do("PATCH", "/api/instances/other/shared-mods", `{"use":"pick"}`)
	writeOwnMod(t, filepath.Join(dirs["other"], "Mods", "Sk8r-BBCity-0.9.0"), "0.9.0") // an older copy of its own

	st := p.install("/api/shared-mods", `{"package":"Sk8r-BBCity","version":"1.0.0"}`)
	if st.Phase != "done" || st.Folder != "Sk8r-BBCity-1.0.0" || downloads.Load() != 1 {
		t.Fatalf("install: %+v", st)
	}
	f := "Sk8r-BBCity-1.0.0"
	if !exists(filepath.Join(shared, "Mods", f, "reskate-levels.json")) {
		t.Fatal("not in the shared mods")
	}
	if !isLink(filepath.Join(dirs["lobby"], "Mods", f)) || !isLink(filepath.Join(dirs["mapday"], "DisabledMods", f)) ||
		!isLink(filepath.Join(dirs["other"], "DisabledMods", f)) || exists(filepath.Join(dirs["off"], "Mods", f)) || exists(filepath.Join(dirs["off"], "DisabledMods", f)) {
		t.Error("links: want it loaded on lobby, off on mapday and other, and nothing on off")
	}

	// Installing it on the servers that pick their shared mods turns their link on.
	for _, id := range []string{"mapday", "other"} {
		code, b := p.do("POST", "/api/instances/"+id+"/mods/installs", `{"package":"Sk8r-BBCity","version":"1.0.0"}`)
		if code != 200 || !strings.Contains(string(b), `"linked":true`) {
			t.Fatalf("load the shared copy on %s: %d %s", id, code, b)
		}
		if !isLink(filepath.Join(dirs[id], "Mods", f)) || exists(filepath.Join(dirs[id], "DisabledMods", f)) {
			t.Errorf("%s does not load the shared copy", id)
		}
	}
	if exists(filepath.Join(dirs["other"], "Mods", "Sk8r-BBCity-0.9.0")) || downloads.Load() != 1 {
		t.Error("other kept its own older copy, or the shared copy was downloaded again")
	}
	if code, b := p.do("POST", "/api/shared-mods/installs", `{"package":"Sk8r-BBCity","version":"1.0.0"}`); code != 409 {
		t.Errorf("the same version again: %d %s, want 409", code, b)
	}
	// A server that loads it from the shared mods cannot install its own.
	if code, b := p.do("POST", "/api/instances/lobby/mods/installs", `{"package":"Sk8r-BBCity"}`); code != 409 {
		t.Errorf("own copy over a shared one: %d %s, want 409", code, b)
	}

	// A newer version takes its place; each link follows, on or off as it was.
	if st := p.install("/api/shared-mods", `{"package":"Sk8r-BBCity"}`); st.Phase != "done" || st.Folder != "Sk8r-BBCity-1.1.0" {
		t.Fatalf("newer: %+v", st)
	}
	f = "Sk8r-BBCity-1.1.0"
	if exists(filepath.Join(shared, "Mods", "Sk8r-BBCity-1.0.0")) || !isLink(filepath.Join(dirs["mapday"], "Mods", f)) || !isLink(filepath.Join(dirs["lobby"], "Mods", f)) {
		t.Error("the links did not follow the newer version")
	}
	// A version the shared mods do not hold is the server's own copy, as on a
	// server that does not use them; one that loads the shared copy cannot.
	if code, b := p.do("POST", "/api/instances/mapday/mods/installs", `{"package":"Sk8r-BBCity","version":"1.0.0"}`); code != 409 {
		t.Errorf("own copy where the shared one loads: %d %s, want 409", code, b)
	}
	if st := p.install("/api/instances/off/mods", `{"package":"Sk8r-BBCity","version":"1.0.0"}`); st.Phase != "done" || isLink(filepath.Join(dirs["off"], "Mods", "Sk8r-BBCity-1.0.0")) {
		t.Errorf("own copy on a server that does not use the shared mods: %+v", st)
	}

	p.signIn("viewer")
	if code, b := p.do("POST", "/api/shared-mods/installs", `{"package":"Sk8r-BBCity"}`); code != 403 {
		t.Errorf("viewer install: %d %s, want 403", code, b)
	}
}

// A job that fails says why, and leaves no download behind.
func TestInstallModFails(t *testing.T) {
	ts, _ := fakeStore(t, storeMod{"Sk8r-Broken", "1.0.0", []byte("not a zip"), false})
	p, dir := newModsPanel(t, ts)
	if st := p.install("/api/instances/a/mods", `{"package":"Sk8r-Broken"}`); st.Phase != "failed" || !strings.Contains(st.Error, "not a zip") {
		t.Fatalf("broken zip: %+v", st)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".mod-*")); len(left) > 0 {
		t.Fatalf("left behind: %v", left)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "Mods")); len(entries) > 0 {
		t.Fatalf("installed something: %v", entries)
	}
}

// Updating every mod queues a job for each one with a newer version, run one
// after another; a server's links to shared mods are the shared mods' to update.
func TestUpdateAllMods(t *testing.T) {
	manifest := func(name, version string) string { return `{"name":"` + name + `","version_number":"` + version + `"}` }
	ts, downloads := fakeStore(t,
		storeMod{pkg: "Sk8r-BBCity", version: "1.1.0", zip: bbZip("1.1.0")}, storeMod{pkg: "Sk8r-BBCity", version: "1.0.0", zip: bbZip("1.0.0")},
		storeMod{pkg: "Sk8r-Park", version: "2.0.0", zip: zipOf("manifest.json", manifest("Park", "2.0.0"))},
		storeMod{pkg: "Sk8r-Ramp", version: "1.0.0", zip: zipOf("manifest.json", manifest("Ramp", "1.0.0"))},
		storeMod{pkg: "Sk8r-Pipe", version: "1.1.0", zip: zipOf("manifest.json", manifest("Pipe", "1.1.0"))})
	shared := filepath.Join(t.TempDir(), "shared")
	p, dirs := newServersPanel(t, ts, shared, "a")
	dir := dirs["a"]
	for path, m := range map[string]string{
		filepath.Join(dir, "Mods", "Sk8r-BBCity-1.0.0"):       manifest("BBCity", "1.0.0"),
		filepath.Join(dir, "DisabledMods", "Sk8r-Park-1.0.0"): manifest("Park", "1.0.0"),
		filepath.Join(dir, "Mods", "Sk8r-Ramp-1.0.0"):         manifest("Ramp", "1.0.0"), // up to date
		filepath.Join(shared, "Mods", "Sk8r-Pipe-1.0.0"):      manifest("Pipe", "1.0.0"),
	} {
		writeFile(t, filepath.Join(path, "manifest.json"), m)
	}
	if code, b := p.do("PATCH", "/api/instances/a/shared-mods", `{"use":"all"}`); code != 200 {
		t.Fatalf("use the shared mods: %d %s", code, b)
	}

	type answer struct {
		Jobs    []updateJob
		Running int
	}
	code, b := p.do("POST", "/api/instances/a/mods/updates", "")
	var got answer
	if json.Unmarshal(b, &got); code != http.StatusAccepted || len(got.Jobs) != 2 ||
		got.Jobs[0].Folder != "Sk8r-BBCity-1.0.0" || got.Jobs[0].Version != "1.1.0" || got.Jobs[1].Folder != "Sk8r-Park-1.0.0" || got.Jobs[1].Version != "2.0.0" {
		t.Fatalf("update all: %d %s", code, b)
	}
	// Asked again meanwhile, the updates under way aren't started twice.
	var again answer
	if code, b := p.do("POST", "/api/instances/a/mods/updates", ""); code != 200 || json.Unmarshal(b, &again) != nil || len(again.Jobs) != 0 {
		t.Errorf("asked again: %d %s", code, b)
	}
	// The second waits for the first.
	base := "/api/instances/a/mods/installs/"
	_, second := p.do("GET", base+got.Jobs[1].ID, "")
	_, first := p.do("GET", base+got.Jobs[0].ID, "")
	if !strings.Contains(string(second), `"queued"`) && !strings.Contains(string(first), `"done"`) {
		t.Errorf("ran side by side: %s and %s", first, second)
	}
	for i, want := range []string{"Sk8r-BBCity-1.1.0", "Sk8r-Park-2.0.0"} {
		if st := p.waitJob(base + got.Jobs[i].ID); st.Phase != "done" || st.Folder != want {
			t.Errorf("job %d: %+v", i, st)
		}
	}
	if !exists(filepath.Join(dir, "Mods", "Sk8r-BBCity-1.1.0")) || !exists(filepath.Join(dir, "DisabledMods", "Sk8r-Park-2.0.0")) ||
		exists(filepath.Join(dir, "Mods", "Sk8r-BBCity-1.0.0")) || exists(filepath.Join(dir, "DisabledMods", "Sk8r-Park-1.0.0")) {
		t.Error("not updated in place, enabled or disabled as they were")
	}
	if !isLink(filepath.Join(dir, "Mods", "Sk8r-Pipe-1.0.0")) || downloads.Load() != 2 {
		t.Errorf("touched the shared mod, or downloaded %d times", downloads.Load())
	}
	if code, b := p.do("POST", "/api/instances/a/mods/updates", ""); code != 200 || json.Unmarshal(b, &got) != nil || len(got.Jobs) != 0 {
		t.Errorf("nothing left to update: %d %s", code, b)
	}
	if _, b := p.do("GET", "/api/audit", ""); strings.Count(string(b), `"mod.update"`) != 2 {
		t.Errorf("audit: %s", b)
	}

	code, b = p.do("POST", "/api/shared-mods/updates", "")
	if json.Unmarshal(b, &got); code != http.StatusAccepted || len(got.Jobs) != 1 || got.Jobs[0].Folder != "Sk8r-Pipe-1.0.0" {
		t.Fatalf("update all shared: %d %s", code, b)
	}
	if st := p.waitJob("/api/shared-mods/installs/" + got.Jobs[0].ID); st.Phase != "done" || !isLink(filepath.Join(dir, "Mods", "Sk8r-Pipe-1.1.0")) {
		t.Errorf("shared update: %+v", st)
	}

	p.signIn("viewer")
	for _, path := range []string{"/api/instances/a/mods/updates", "/api/shared-mods/updates"} {
		if code, _ := p.do("POST", path, ""); code != http.StatusForbidden {
			t.Errorf("viewer %s: %d, want 403", path, code)
		}
	}
}
