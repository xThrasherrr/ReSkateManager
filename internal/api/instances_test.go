package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/xThrasherrr/ReSkateManager/internal/auth"
	"github.com/xThrasherrr/ReSkateManager/internal/instance"
	"github.com/xThrasherrr/ReSkateManager/internal/serverconfig"
	"github.com/xThrasherrr/ReSkateManager/internal/store"
)

// Removing a server keeps its files unless asked to purge them, and a purge
// refuses a folder that holds the manager's or another server's files.
func TestDeleteInstancePurge(t *testing.T) {
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
	servers, data := t.TempDir(), t.TempDir()
	for id, dir := range map[string]string{"all": servers, "a": filepath.Join(servers, "a"), "b": filepath.Join(servers, "b"), "data": data} {
		if err := os.MkdirAll(filepath.Join(dir, "Mods"), 0o755); err != nil {
			t.Fatal(err)
		}
		d := instance.Def{ID: id, Name: id, Dir: dir}
		if err := st.CreateInstance(ctx, d); err != nil {
			t.Fatal(err)
		}
		reg.Add(d)
	}
	if err := st.SeenPlayer(ctx, "a", "76561198000000001", "skater"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer((&API{Store: st, Auth: svc, Reg: reg, Log: log, ServersDir: servers,
		ConfigPath: filepath.Join(data, "manager.toml"), Static: fstest.MapFS{}}).Handler())
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	p := &panel{t: t, url: srv.URL, client: &http.Client{Jar: jar}, auth: svc}
	p.post("/api/auth/login", `{"username":"owner","password":"`+testPassword+`"}`)

	if code, b := p.do("DELETE", "/api/instances/all?purge=1", ""); code != 400 {
		t.Fatalf("purge the servers folder: %d %s, want 400", code, b)
	}
	if code, b := p.do("DELETE", "/api/instances/a?purge=1", ""); code != 400 {
		t.Fatalf("purge a folder inside another server's: %d %s, want 400", code, b)
	}
	if code, b := p.do("DELETE", "/api/instances/data?purge=1", ""); code != 400 {
		t.Fatalf("purge the manager's data folder: %d %s, want 400", code, b)
	}
	if code, b := p.do("DELETE", "/api/instances/all", ""); code != 200 {
		t.Fatalf("remove: %d %s", code, b)
	}
	if _, err := os.Stat(servers); err != nil {
		t.Fatal("a plain remove deleted files:", err)
	}

	if code, b := p.do("DELETE", "/api/instances/a?purge=1", ""); code != 200 {
		t.Fatalf("purge: %d %s", code, b)
	}
	if _, err := os.Stat(filepath.Join(servers, "a")); !os.IsNotExist(err) {
		t.Fatalf("purged folder still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(servers, "b", "Mods")); err != nil {
		t.Fatal("purge touched another server's files:", err)
	}
	if hist, err := st.PlayerHistory(ctx, "a", "", 10); err != nil || len(hist) != 0 {
		t.Fatalf("player history after purge: %v %v", hist, err)
	}
	if _, ok := reg.Get("a"); ok {
		t.Fatal("purged server still registered")
	}
}

// Only owners may put a server outside the servers folder, and no server may
// sit in another's Mods folder, where a mod upload could plant its program.
func TestCreateInstanceFolders(t *testing.T) {
	servers, elsewhere := t.TempDir(), t.TempDir()
	p := newPanel(t, func(a *API) {
		a.Reg = instance.NewRegistry(a.Log)
		a.ServersDir = servers
	})
	p.post("/api/auth/login", `{"username":"owner","password":"`+testPassword+`"}`)
	code, b := p.do("POST", "/api/roles", `{"name":"Builder","permissions":["instances.manage","settings.edit"]}`)
	if code != http.StatusOK {
		t.Fatalf("create role: %d %s", code, b)
	}
	var builder auth.Role
	json.Unmarshal(b, &builder)
	p.createUser("builder", grant(builder.ID))

	create := func(name, dir string) (int, []byte) {
		body, _ := json.Marshal(map[string]string{"name": name, "dir": dir})
		return p.do("POST", "/api/instances", string(body))
	}
	a := filepath.Join(servers, "a")
	cases := []struct {
		who, what, name, dir string
		want                 int
	}{
		{"owner", "a default folder", "a", "", 201},
		{"owner", "a folder elsewhere", "b", filepath.Join(elsewhere, "Mods", "b"), 201},
		{"owner", "a folder in a's Mods", "c", filepath.Join(a, "Mods", "evil"), 400},
		{"owner", "a folder in a's DisabledMods", "c", filepath.Join(a, "DisabledMods", "evil"), 400},
		{"owner", "a folder whose Mods holds b", "c", elsewhere, 400},
		{"builder", "a default folder", "d", "", 201},
		{"builder", "a folder in the servers folder", "e", filepath.Join(servers, "e"), 201},
		{"builder", "a folder elsewhere", "f", filepath.Join(elsewhere, "f"), 403},
		{"builder", "the servers folder itself", "f", servers, 403},
		{"builder", "a folder in a's Mods", "f", filepath.Join(a, "Mods", "evil"), 400},
	}
	signedIn := "owner"
	for _, c := range cases {
		if c.who != signedIn {
			p.post("/api/auth/logout", `{}`)
			p.post("/api/auth/login", `{"username":"`+c.who+`","password":"`+testPassword+`"}`)
			signedIn = c.who
		}
		if code, b := create(c.name, c.dir); code != c.want {
			t.Errorf("%s, %s: %d %s, want %d", c.who, c.what, code, b, c.want)
		}
	}
}

// A panel name can hold any characters, but the server refuses to start with
// a name outside its rule, so the new config gets a cleaned copy.
func TestCreateInstanceServerName(t *testing.T) {
	servers := t.TempDir()
	p := newPanel(t, func(a *API) {
		a.Reg = instance.NewRegistry(a.Log)
		a.ServersDir = servers
	})
	p.signIn("owner")
	code, b := p.do("POST", "/api/instances", `{"name":"Thrasher's Park!"}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, b)
	}
	var v struct{ ID, Name string }
	json.Unmarshal(b, &v)
	f, err := serverconfig.Read(filepath.Join(servers, v.ID, "ReSkateServer.json"))
	if err != nil {
		t.Fatal(err)
	}
	if name, _ := f.Get("name"); v.Name != "Thrasher's Park!" || name != "Thrashers Park" {
		t.Errorf("panel name %q, server name %q", v.Name, name)
	}
}

// The folder checks look where symlinks lead, so a link can't carry a server
// out of the servers folder or into another's Mods, or put the manager's own
// files inside a folder being purged.
func TestFolderChecksFollowLinks(t *testing.T) {
	servers, elsewhere := t.TempDir(), t.TempDir()
	data := filepath.Join(elsewhere, "sub", "data")
	for _, d := range []string{filepath.Join(servers, "a", "Mods"), data} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, to := range map[string]string{
		"out":   elsewhere,
		"self":  servers,
		"same":  filepath.Join(servers, "a"),
		"amods": filepath.Join(servers, "a", "Mods"),
	} {
		if err := os.Symlink(to, filepath.Join(servers, name)); err != nil {
			t.Skip("can't make symlinks here:", err)
		}
	}
	p := newPanel(t, func(a *API) {
		a.Reg = instance.NewRegistry(a.Log)
		a.ServersDir = servers
		a.ConfigPath = filepath.Join(data, "manager.toml")
	})
	p.signIn("owner")
	code, b := p.do("POST", "/api/roles", `{"name":"Builder","permissions":["instances.manage"]}`)
	if code != http.StatusOK {
		t.Fatalf("create role: %d %s", code, b)
	}
	var builder auth.Role
	json.Unmarshal(b, &builder)
	p.createUser("builder", grant(builder.ID))

	for _, c := range []struct {
		who, what, name, dir string
		want                 int
	}{
		{"owner", "a default folder", "a", "", 201},
		{"owner", "a's folder through a link", "b", filepath.Join(servers, "same"), 400},
		{"owner", "a's Mods through a link", "b", filepath.Join(servers, "amods", "evil"), 400},
		{"owner", "a folder that holds the manager's data", "s", filepath.Join(servers, "out", "sub"), 201},
		{"builder", "a folder out of the servers folder through a link", "c", filepath.Join(servers, "out", "c"), 403},
		{"builder", "the servers folder through a link", "c", filepath.Join(servers, "self"), 403},
	} {
		p.signIn(c.who)
		body, _ := json.Marshal(map[string]string{"name": c.name, "dir": c.dir})
		if code, b := p.do("POST", "/api/instances", string(body)); code != c.want {
			t.Errorf("%s, %s: %d %s, want %d", c.who, c.what, code, b, c.want)
		}
	}

	p.signIn("owner")
	if code, b := p.do("DELETE", "/api/instances/s?purge=1", ""); code != 400 {
		t.Errorf("purge a folder that holds the manager's data: %d %s, want 400", code, b)
	}
	if _, err := os.Stat(data); err != nil {
		t.Fatal("purge deleted the manager's data:", err)
	}
}

// The console holds commands that do what another page does to that page's
// permission, and refuses an empty line instead of failing on it.
func TestConsolePermissions(t *testing.T) {
	p := newPanel(t, func(a *API) {
		a.Reg = instance.NewRegistry(a.Log)
		a.ServersDir = t.TempDir()
	})
	p.post("/api/auth/login", `{"username":"owner","password":"`+testPassword+`"}`)
	if code, b := p.do("POST", "/api/instances", `{"name":"a"}`); code != 201 {
		t.Fatalf("create: %d %s", code, b)
	}
	if code, b := p.do("POST", "/api/instances/a/command", `{"line":"   "}`); code != 400 {
		t.Errorf("empty command: %d %s, want 400", code, b)
	}
	code, b := p.do("POST", "/api/roles", `{"name":"Console","permissions":["console.view","console.exec"]}`)
	if code != http.StatusOK {
		t.Fatalf("create role: %d %s", code, b)
	}
	var role auth.Role
	json.Unmarshal(b, &role)
	p.createUser("operator", grant(role.ID))
	p.post("/api/auth/logout", `{}`)
	p.post("/api/auth/login", `{"username":"operator","password":"`+testPassword+`"}`)
	for line, want := range map[string]int{
		"password hunter2":   403,
		"MAP Isle of Grom":   403,
		"layers x=on":        403,
		"layer x on":         403,
		"kick 1":             403,
		"msg skater hi":      403,
		"noclip-allow on":    403, // the in-game menu's name for noclip
		"tuning-enforce off": 403,
		"frobnicate":         403, // unknown: maybe a newer server's setting
		"status":             400, // allowed, but the server is not running
		"players":            400,
		"tpall":              400,
	} {
		body, _ := json.Marshal(map[string]string{"line": line})
		if code, b := p.do("POST", "/api/instances/a/command", string(body)); code != want {
			t.Errorf("%q: %d %s, want %d", line, code, b, want)
		}
	}
}

// Someone who may only manage servers sees every one, so they can edit,
// export and remove the servers they create.
func TestInstancesManageSeesServers(t *testing.T) {
	p := newPanel(t, func(a *API) {
		a.Reg = instance.NewRegistry(a.Log)
		a.ServersDir = t.TempDir()
	})
	p.post("/api/auth/login", `{"username":"owner","password":"`+testPassword+`"}`)
	code, b := p.do("POST", "/api/roles", `{"name":"Maker","permissions":["instances.manage"]}`)
	if code != http.StatusOK {
		t.Fatalf("create role: %d %s", code, b)
	}
	var maker auth.Role
	json.Unmarshal(b, &maker)
	p.createUser("maker", grant(maker.ID))
	p.post("/api/auth/logout", `{}`)
	p.post("/api/auth/login", `{"username":"maker","password":"`+testPassword+`"}`)
	if code, b := p.do("POST", "/api/instances", `{"name":"made"}`); code != 201 {
		t.Fatalf("create: %d %s", code, b)
	}
	if code, b := p.do("PATCH", "/api/instances/made", `{"name":"renamed"}`); code != 200 {
		t.Errorf("rename the server they made: %d %s", code, b)
	}
	if code, b := p.do("GET", "/api/instances/made/settings", ""); code != 403 {
		t.Errorf("read its settings without settings.view: %d %s", code, b)
	}
}

// Removing a server forgets everything the panel kept about it, so another
// server added later under the same ID starts clean.
func TestDeleteInstanceForgetsIt(t *testing.T) {
	var st *store.Store
	p := newPanel(t, func(a *API) {
		a.Reg = instance.NewRegistry(a.Log)
		a.ServersDir = t.TempDir()
		st = a.Store
	})
	ctx := context.Background()
	p.post("/api/auth/login", `{"username":"owner","password":"`+testPassword+`"}`)
	if code, b := p.do("POST", "/api/instances", `{"name":"lobby"}`); code != 201 {
		t.Fatalf("create: %d %s", code, b)
	}
	if err := st.SeenPlayer(ctx, "lobby", "76561198000000001", "skater"); err != nil {
		t.Fatal(err)
	}
	p.createUser("mod", fmt.Sprintf(`{"roleId":%d,"instance":"lobby"}`, p.roleID("Moderator")))
	if code, b := p.do("DELETE", "/api/instances/lobby", ""); code != 200 {
		t.Fatalf("remove: %d %s", code, b)
	}
	if hist, err := st.PlayerHistory(ctx, "lobby", "", 10); err != nil || len(hist) != 0 {
		t.Errorf("player history kept: %v %v", hist, err)
	}
	var grants int
	st.DB.QueryRow("SELECT COUNT(*) FROM user_roles WHERE instance_id = 'lobby'").Scan(&grants)
	if grants != 0 {
		t.Errorf("%d grants kept on the removed server", grants)
	}
}
