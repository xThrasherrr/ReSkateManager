package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xThrasherrr/ReSkateManager/internal/modprofile"
)

// send posts body as contentType, as the panel sends a profile file.
func (p *panel) send(path, contentType string, body []byte) (int, []byte) {
	p.t.Helper()
	req, _ := http.NewRequest("POST", p.url+path, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-RSM", "1")
	resp, err := p.client.Do(req)
	if err != nil {
		p.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// A server shares the maps it loads that Thunderstore has, at its versions,
// as a file and as a code, which the same list keeps for a while.
func TestShareProfile(t *testing.T) {
	ts, _ := fakeStore(t,
		storeMod{"Sk8r-BBCity", "1.1.0", bbZip("1.1.0"), false},
		storeMod{"Sk8r-BBCity", "1.0.0", bbZip("1.0.0"), false},
		storeMod{"Drip-Deck", "2.0.0", zipOf("manifest.json", `{"name":"Deck"}`, "Win32/deck.cas", "x"), false},
	)
	p, dir := newModsPanel(t, ts)
	for _, body := range []string{`{"package":"Sk8r-BBCity","version":"1.0.0"}`, `{"package":"Drip-Deck"}`} {
		if st := p.install("/api/instances/a/mods", body); st.Phase != "done" {
			t.Fatalf("install %s: %+v", body, st)
		}
	}
	// A map uploaded as a zip, which players can't fetch.
	park := filepath.Join(dir, "Mods", "MyPark")
	os.MkdirAll(park, 0o755)
	os.WriteFile(filepath.Join(park, "reskate-levels.json"), []byte(`{"levels":[{"asset":"Levels/Custom/MyPark/MyPark_LevelRoot"}]}`), 0o644)
	os.WriteFile(filepath.Join(dir, "ReSkateServer.json"), []byte(`{"server": {"name": "Grom / Street"}}`), 0o644)

	p.signIn("viewer")
	code, b := p.do("GET", "/api/instances/a/mods/profile", "")
	var sp sharedProfile
	if json.Unmarshal(b, &sp); code != 200 {
		t.Fatalf("profile: %d %s", code, b)
	}
	if want := []profileMod{{Package: "Sk8r-BBCity", Version: "1.0.0", Title: "BBCity"}}; sp.Name != "Grom  Street" || !reflect.DeepEqual(sp.Mods, want) ||
		!reflect.DeepEqual(sp.Left, []leftOut{{"MyPark", "not on Thunderstore"}}) {
		t.Errorf("profile %+v", sp)
	}

	req, _ := http.NewRequest("GET", p.url+"/api/instances/a/mods/profile.r2z", nil)
	resp, err := p.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	file, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	want := modprofile.Profile{Name: "Grom  Street", Mods: []modprofile.Mod{{Name: "Sk8r-BBCity", Version: "1.0.0", Enabled: true}}}
	if got, err := modprofile.ParseArchive(file); err != nil || !reflect.DeepEqual(got, want) ||
		!strings.Contains(resp.Header.Get("Content-Disposition"), `filename="Grom  Street.r2z"`) {
		t.Errorf("file %+v, %v, %q", got, err, resp.Header.Get("Content-Disposition"))
	}

	var first, again struct{ Code string }
	if code, b := p.do("POST", "/api/instances/a/mods/profile/code", ""); json.Unmarshal(b, &first) != nil || code != 200 {
		t.Fatalf("code: %d %s", code, b)
	}
	if _, b := p.do("POST", "/api/instances/a/mods/profile/code", ""); json.Unmarshal(b, &again) != nil || again.Code != first.Code {
		t.Errorf("the same profile got codes %q and %q", first.Code, again.Code)
	}
	payload, err := ts.FetchProfile(context.Background(), first.Code)
	if got, perr := modprofile.ParsePayload(payload); err != nil || perr != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("code %s holds %+v (%v, %v)", first.Code, got, err, perr)
	}

	// Without a map it loads from Thunderstore, there is nothing to share.
	p.signIn("owner")
	if code, b := p.do("PATCH", "/api/instances/a/mods/Sk8r-BBCity-1.0.0", `{"enabled":false}`); code != 200 {
		t.Fatalf("disable: %d %s", code, b)
	}
	if code, b := p.do("POST", "/api/instances/a/mods/profile/code", ""); code != http.StatusConflict {
		t.Errorf("an empty profile shared: %d %s", code, b)
	}
}

// A profile read from a code or a file says how each mod stands on the
// server: new, another version to replace, installed, or not on
// Thunderstore, with the maps it adds once someone has looked.
func TestReadProfile(t *testing.T) {
	ts, _ := fakeStore(t,
		storeMod{"Sk8r-BBCity", "1.1.0", bbZip("1.1.0"), false},
		storeMod{"Sk8r-BBCity", "1.0.0", bbZip("1.0.0"), false},
		storeMod{"Drip-Deck", "2.0.0", zipOf("manifest.json", `{"name":"Deck"}`, "Win32/deck.cas", "x"), false},
	)
	p, _ := newModsPanel(t, ts)
	if st := p.install("/api/instances/a/mods", `{"package":"Sk8r-BBCity","version":"1.0.0"}`); st.Phase != "done" {
		t.Fatalf("install: %+v", st)
	}
	prof := modprofile.Profile{Name: "Tom's", Mods: []modprofile.Mod{
		{Name: "Sk8r-BBCity", Version: "1.1.0", Enabled: true},
		{Name: "drip-deck", Version: "9.9.9", Enabled: false}, // a version Thunderstore hasn't: the newest
		{Name: "Gone-Mod", Version: "1.0.0", Enabled: true},
	}}
	check := func(what string, code int, b []byte, deckMaps bool) {
		t.Helper()
		var got struct {
			Name string
			Mods []profileEntry
		}
		if json.Unmarshal(b, &got); code != 200 || got.Name != "Tom's" || len(got.Mods) != 3 {
			t.Fatalf("%s: %d %s", what, code, b)
		}
		bb, deck, gone := got.Mods[0], got.Mods[1], got.Mods[2]
		if bb.State != "replaces" || bb.Replaces != "1.0.0" || bb.Install != "1.1.0" || bb.Package != "Sk8r-BBCity" {
			t.Errorf("%s: BBCity %+v", what, bb)
		}
		if deck.State != "new" || deck.Install != "2.0.0" || deck.Package != "Drip-Deck" || deck.Enabled || (deck.Maps != nil) != deckMaps {
			t.Errorf("%s: Deck %+v", what, deck)
		}
		if gone.State != "absent" || gone.Package != "" {
			t.Errorf("%s: Gone %+v", what, gone)
		}
	}
	file, _ := prof.Archive()
	code, b := p.send("/api/instances/a/mods/profile/read", "application/octet-stream", file)
	check("file", code, b, false)

	// Shared as a code, as a player's launcher does; a look inside the Deck
	// shows it adds no maps.
	payload, _ := prof.Payload()
	key, err := ts.ShareProfile(context.Background(), payload)
	if err != nil {
		t.Fatal(err)
	}
	if code, b := p.do("GET", "/api/thunderstore/maps?package=Drip-Deck&version=2.0.0", ""); code != 200 {
		t.Fatalf("maps: %d %s", code, b)
	}
	code, b = p.do("POST", "/api/instances/a/mods/profile/read", `{"code":" `+key+` "}`)
	check("code", code, b, true)

	// Now 1.1.0 is in, it's installed.
	if st := p.install("/api/instances/a/mods", `{"package":"Sk8r-BBCity","version":"1.1.0"}`); st.Phase != "done" {
		t.Fatalf("update: %+v", st)
	}
	code, b = p.send("/api/instances/a/mods/profile/read", "application/octet-stream", file)
	var got struct{ Mods []profileEntry }
	if json.Unmarshal(b, &got); code != 200 || got.Mods[0].State != "installed" || !strings.Contains(got.Mods[0].Note, "already has") {
		t.Errorf("after the update: %d %s", code, b)
	}

	for body, want := range map[string]int{
		`{"code":"nope"}`:          400,
		`{"code":"0000abcd-dead"}`: http.StatusBadGateway, // no such profile
	} {
		if code, b := p.do("POST", "/api/instances/a/mods/profile/read", body); code != want {
			t.Errorf("%s: %d %s, want %d", body, code, b, want)
		}
	}
	if code, b := p.send("/api/instances/a/mods/profile/read", "application/octet-stream", []byte("not a profile")); code != 400 {
		t.Errorf("junk: %d %s", code, b)
	}
}
