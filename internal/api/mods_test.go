package api

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/xThrasherrr/ReSkateManager/internal/alerts"
	"github.com/xThrasherrr/ReSkateManager/internal/auth"
	"github.com/xThrasherrr/ReSkateManager/internal/instance"
	"github.com/xThrasherrr/ReSkateManager/internal/store"
	"github.com/xThrasherrr/ReSkateManager/internal/thunderstore"
)

// serveListing answers for Thunderstore's listing index, from the test
// server at base: a redirect to a gzipped list of chunk addresses, and one
// chunk holding pkgs, a JSON array of packages. It reports whether r was for
// the listing.
func serveListing(w http.ResponseWriter, r *http.Request, base, pkgs string) bool {
	switch r.URL.Path {
	case "/c/reskate/api/v1/package-listing-index/":
		http.Redirect(w, r, base+"/listing/index", http.StatusFound)
	case "/listing/index":
		writeGzip(w, `["`+base+`/listing/0"]`)
	case "/listing/0":
		writeGzip(w, pkgs)
	default:
		return false
	}
	return true
}

func writeGzip(w io.Writer, s string) {
	zw := gzip.NewWriter(w)
	zw.Write([]byte(s))
	zw.Close()
}

// newModsPanel serves a panel with one server, "a", in the returned folder,
// signed in as the owner. A viewer account exists too.
func newModsPanel(t *testing.T, ts *thunderstore.Client) (*panel, string) {
	t.Helper()
	p, dirs := newServersPanel(t, ts, "", "a")
	return p, dirs["a"]
}

// newServersPanel serves a panel with a server, in its own folder, for each
// id, and the shared mods in shared (none when empty). It is signed in as the
// owner; a viewer account, with the Viewer role on every server, exists too.
func newServersPanel(t *testing.T, ts *thunderstore.Client, shared string, ids ...string) (*panel, map[string]string) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	svc, err := auth.New(ctx, st.DB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Setup(ctx, "127.0.0.1", svc.SetupPIN(), "owner", testPassword); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	reg := instance.NewRegistry(log)
	dirs := map[string]string{}
	for _, id := range ids {
		dirs[id] = t.TempDir()
		d := instance.Def{ID: id, Name: id, Dir: dirs[id]}
		if err := st.CreateInstance(ctx, d); err != nil {
			t.Fatal(err)
		}
		reg.Add(d)
	}
	a := &API{Store: st, Auth: svc, Reg: reg, Log: log, Static: fstest.MapFS{}, Thunderstore: ts, SharedDir: shared,
		Alerts: &alerts.Notifier{Store: st, Log: log}}
	srv := httptest.NewServer(a.Handler())
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	p := &panel{t: t, url: srv.URL, client: &http.Client{Jar: jar}, auth: svc, api: a}
	p.post("/api/auth/login", `{"username":"owner","password":"`+testPassword+`"}`)
	if id := p.roleID("Viewer"); id != 0 {
		p.createUser("viewer", grant(id))
	}
	return p, dirs
}

// roleID finds a role by name.
func (p *panel) roleID(name string) int64 {
	p.t.Helper()
	_, b := p.do("GET", "/api/roles", "")
	var roles []auth.Role
	json.Unmarshal(b, &roles)
	for _, r := range roles {
		if r.Name == name {
			return r.ID
		}
	}
	return 0
}

func (p *panel) signIn(name string) {
	p.post("/api/auth/logout", `{}`)
	p.post("/api/auth/login", `{"username":"`+name+`","password":"`+testPassword+`"}`)
}

// uploadMod sends zipped to server a's mods in chunks of n bytes, the first
// one twice as a retry would, and returns the last answer.
func (p *panel) uploadMod(name string, zipped []byte, n int) (int, string) {
	p.t.Helper()
	return p.uploadTo("/api/instances/a/mods/uploads", name, zipped, n)
}

// uploadTo sends zipped in chunks of n bytes to the uploads at base.
func (p *panel) uploadTo(base, name string, zipped []byte, n int) (int, string) {
	p.t.Helper()
	code, b := p.do("POST", base, fmt.Sprintf(`{"name":%q,"size":%d}`, name, len(zipped)))
	if code != 200 {
		return code, string(b)
	}
	var up struct{ ID string }
	json.Unmarshal(b, &up)
	put := func(off int) (int, []byte) {
		return p.do("PUT", fmt.Sprintf("%s/%s?offset=%d", base, up.ID, off), string(zipped[off:min(off+n, len(zipped))]))
	}
	if code, b := put(0); code != 200 {
		return code, string(b)
	}
	for off := 0; off < len(zipped); off += n {
		if code, b = put(off); code != 200 {
			break
		}
	}
	return code, string(b)
}

// Uploading a mod needs settings.edit; a viewer can list mods but not add one.
func TestUploadMod(t *testing.T) {
	p, dir := newModsPanel(t, nil)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("bbcity/reskate-levels.json")
	w.Write([]byte(`{"levels":[{"asset":"Levels/Custom/BBCity/BBCity_LevelRoot"}]}`))
	zw.Close()

	var got struct{ Folder string }
	code, b := p.uploadMod("bbcity.zip", buf.Bytes(), 100)
	if st := p.runJob(code, []byte(b), &got); st.Phase != "done" || got.Folder != "bbcity" {
		t.Fatalf("owner upload: %+v, %+v", st, got)
	}
	if _, err := os.Stat(filepath.Join(dir, "Mods", "bbcity", "reskate-levels.json")); err != nil {
		t.Fatal(err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".mod-upload-*")); len(left) > 0 {
		t.Fatalf("upload files left behind: %v", left)
	}

	// Chunks must follow on from what has arrived.
	_, bs := p.do("POST", "/api/instances/a/mods/uploads", `{"name":"x.zip","size":10}`)
	var up struct{ ID string }
	json.Unmarshal(bs, &up)
	if code, b := p.do("PUT", "/api/instances/a/mods/uploads/"+up.ID+"?offset=5", "12345"); code != 409 {
		t.Fatalf("chunk past the end: %d %s, want 409", code, b)
	}
	if code, b := p.do("POST", "/api/instances/a/mods/uploads", `{"name":"x.zip","size":3000000000}`); code != 413 {
		t.Fatalf("huge upload: %d %s, want 413", code, b)
	}

	p.signIn("viewer")
	if code, b := p.uploadMod("bbcity.zip", buf.Bytes(), 100); code != 403 {
		t.Fatalf("viewer upload: %d %s, want 403", code, b)
	}
	if code, b := p.do("GET", "/api/instances/a/mods", ""); code != 200 {
		t.Fatalf("viewer list: %d %s", code, b)
	}
}

// Disabling, deleting and updating need settings.edit. An update fetches the
// newest version from Thunderstore and replaces the old folder.
func TestManageMods(t *testing.T) {
	var zipped bytes.Buffer
	zw := zip.NewWriter(&zipped)
	w, _ := zw.Create("manifest.json")
	w.Write([]byte(`{"name":"BBCity","version_number":"1.1.0"}`))
	w, _ = zw.Create("reskate-levels.json")
	w.Write([]byte(`{"levels":[{"asset":"Levels/Custom/BBCity/BBCity_LevelRoot"}]}`))
	zw.Close()
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveListing(w, r, ts.URL, `[{"full_name":"Sk8r-BBCity","package_url":"`+ts.URL+`/p/","versions":[
			{"version_number":"1.1.0","download_url":"`+ts.URL+`/package/download/Sk8r/BBCity/1.1.0/"}]}]`) {
			return
		}
		switch r.URL.Path {
		case "/package/download/Sk8r/BBCity/1.1.0/":
			w.Write(zipped.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)
	client := thunderstore.New("reskate")
	client.Base = ts.URL

	p, dir := newModsPanel(t, client)
	for _, m := range []string{"Sk8r-BBCity-1.0.0", "other"} {
		os.MkdirAll(filepath.Join(dir, "Mods", m), 0o755)
	}
	os.WriteFile(filepath.Join(dir, "Mods", "Sk8r-BBCity-1.0.0", "manifest.json"), []byte(`{"name":"BBCity","version_number":"1.0.0"}`), 0o644)

	code, b := p.do("GET", "/api/instances/a/mods/updates", "")
	var updates map[string]modUpdate
	json.Unmarshal(b, &updates)
	if u := updates["Sk8r-BBCity-1.0.0"]; code != 200 || len(updates) != 1 || u.Latest != "1.1.0" || !u.Newer {
		t.Fatalf("updates: %d %s", code, b)
	}

	p.signIn("viewer")
	for _, c := range [][2]string{{"PATCH", "/mods/other"}, {"DELETE", "/mods/other"}, {"POST", "/mods/Sk8r-BBCity-1.0.0/update"}} {
		if code, b := p.do(c[0], "/api/instances/a"+c[1], `{"enabled":false}`); code != 403 {
			t.Errorf("viewer %s %s: %d %s, want 403", c[0], c[1], code, b)
		}
	}

	p.signIn("owner")
	if code, b := p.do("PATCH", "/api/instances/a/mods/other", `{"enabled":false}`); code != 200 {
		t.Fatalf("disable: %d %s", code, b)
	}
	if _, err := os.Stat(filepath.Join(dir, "DisabledMods", "other")); err != nil {
		t.Fatal(err)
	}
	if code, b := p.do("DELETE", "/api/instances/a/mods/other", ""); code != 200 {
		t.Fatalf("delete: %d %s", code, b)
	}
	if code, _ := p.do("DELETE", "/api/instances/a/mods/other", ""); code != 404 {
		t.Errorf("delete again: %d, want 404", code)
	}
	if code, _ := p.do("DELETE", "/api/instances/a/mods/..%2Fsecret", ""); code != 404 {
		t.Errorf("delete outside Mods: %d, want 404", code)
	}

	id := p.startJob("/api/instances/a/mods/Sk8r-BBCity-1.0.0/update", "")
	if st := p.waitJob("/api/instances/a/mods/installs/" + id); st.Phase != "done" || st.Folder != "Sk8r-BBCity-1.1.0" {
		t.Fatalf("update: %+v", st)
	}
	if _, err := os.Stat(filepath.Join(dir, "Mods", "Sk8r-BBCity-1.1.0", "reskate-levels.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "Mods", "Sk8r-BBCity-1.0.0")); !os.IsNotExist(err) {
		t.Error("the old version survived the update")
	}
	if code, b := p.do("POST", "/api/instances/a/mods/Sk8r-BBCity-1.1.0/update", ""); code != 409 {
		t.Errorf("update when up to date: %d %s, want 409", code, b)
	}
}
