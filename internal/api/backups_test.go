package api

import (
	"archive/zip"
	"bytes"
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
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/auth"
	"github.com/xThrasherrr/ReSkateManager/internal/backup"
	"github.com/xThrasherrr/ReSkateManager/internal/instance"
	"github.com/xThrasherrr/ReSkateManager/internal/store"
)

// newBackupPanel serves a panel that keeps backups, with server "a" in its
// servers folder, signed in as the owner. A viewer account exists too.
func newBackupPanel(t *testing.T) (*panel, *API) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
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
	d := instance.Def{ID: "a", Name: "a", Dir: filepath.Join(root, "servers", "a")}
	if err := st.CreateInstance(ctx, d); err != nil {
		t.Fatal(err)
	}
	reg.Add(d)
	a := &API{Store: st, Auth: svc, Reg: reg, Log: log, Static: fstest.MapFS{}, ServersDir: filepath.Join(root, "servers"),
		Backups: &backup.Service{Dir: filepath.Join(root, "backups"), DataDir: t.TempDir(), Root: root, Version: "test", Store: st, Reg: reg, Log: log}}
	srv := httptest.NewServer(a.Handler())
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	p := &panel{t: t, url: srv.URL, client: &http.Client{Jar: jar}, auth: svc}
	p.post("/api/auth/login", `{"username":"owner","password":"`+testPassword+`"}`)
	p.createUser("viewer", grant(p.roleID("Viewer")))
	return p, a
}

// runJob polls the job a 202 answer started until it finishes, and decodes
// its result into out.
func (p *panel) runJob(code int, b []byte, out any) jobStatus {
	p.t.Helper()
	var started struct{ ID string }
	if json.Unmarshal(b, &started); code != http.StatusAccepted || started.ID == "" {
		p.t.Fatalf("no job started: %d %s", code, b)
	}
	for range 500 {
		code, b := p.do("GET", "/api/jobs/"+started.ID, "")
		var st struct {
			jobStatus
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(b, &st); code != 200 {
			p.t.Fatalf("job: %d %s", code, b)
		}
		if st.Phase != "running" {
			if out != nil {
				json.Unmarshal(st.Result, out)
			}
			return st.jobStatus
		}
		time.Sleep(10 * time.Millisecond)
	}
	p.t.Fatal("the job never finished")
	return jobStatus{}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

// Owners back up, restore a server, download and delete; others can't.
func TestBackupAndRestore(t *testing.T) {
	p, a := newBackupPanel(t)
	in, _ := a.Reg.Get("a")
	dir := in.Def().Dir
	writeFile(t, filepath.Join(dir, "ReSkateServer.json"), `{"name":"Before"}`)
	writeOwnMod(t, filepath.Join(dir, "Mods", "bbcity"), "1.0.0")

	code, b := p.do("POST", "/api/backups", `{"mods":true}`)
	var made backup.Info
	if st := p.runJob(code, b, &made); st.Phase != "done" || made.Name == "" || !made.Database {
		t.Fatalf("backup: %+v %+v", st, made)
	}
	code, b = p.do("GET", "/api/backups", "")
	var list struct {
		Backups  []backup.Info
		Settings backup.Settings
	}
	if json.Unmarshal(b, &list); code != 200 || len(list.Backups) != 1 || list.Settings != backup.DefaultSettings {
		t.Fatalf("list: %d %s", code, b)
	}

	writeFile(t, filepath.Join(dir, "ReSkateServer.json"), `{"name":"After"}`)
	os.RemoveAll(filepath.Join(dir, "Mods", "bbcity"))
	code, b = p.do("GET", "/api/instances/a/backups", "")
	if code != 200 || !strings.Contains(string(b), made.Name) || !strings.Contains(string(b), `"modFiles":true`) {
		t.Fatalf("server backups: %d %s", code, b)
	}
	code, b = p.do("POST", "/api/instances/a/restore", fmt.Sprintf(`{"backup":%q,"mods":true}`, made.Name))
	if st := p.runJob(code, b, nil); st.Phase != "done" {
		t.Fatalf("restore: %+v", st)
	}
	if got := readFile(filepath.Join(dir, "ReSkateServer.json")); got != `{"name":"Before"}` {
		t.Errorf("config after restore: %s", got)
	}
	if !exists(filepath.Join(dir, "Mods", "bbcity", "manifest.json")) {
		t.Error("the mod did not come back")
	}
	if in.State() != instance.Stopped {
		t.Errorf("state after restore: %s", in.State())
	}
	if code, b := p.do("POST", "/api/instances/a/restore", `{"backup":"backup-20260101-000000-manual.zip"}`); code != 404 {
		t.Errorf("restore from a missing backup: %d %s", code, b)
	}

	resp, err := p.client.Get(p.url + "/api/backups/" + made.Name)
	if err != nil {
		t.Fatal(err)
	}
	var zipped bytes.Buffer
	zipped.ReadFrom(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.HasPrefix(zipped.Bytes(), []byte("PK")) {
		t.Errorf("download: %d", resp.StatusCode)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("a backup download may be cached: Cache-Control %q", cc)
	}

	if code, b := p.do("PATCH", "/api/backups", `{"every":12,"keep":3,"mods":true}`); code != 200 || !strings.Contains(string(b), `"keep":3`) {
		t.Errorf("settings: %d %s", code, b)
	}
	if code, _ := p.do("PATCH", "/api/backups", `{"keep":0}`); code != 400 {
		t.Errorf("keep 0: %d", code)
	}

	p.signIn("viewer")
	for _, r := range []struct{ method, path, body string }{
		{"GET", "/api/backups", ""},
		{"GET", "/api/backups/" + made.Name, ""},
		{"POST", "/api/backups", `{}`},
		{"DELETE", "/api/backups/" + made.Name, ""},
		{"POST", "/api/instances/a/restore", fmt.Sprintf(`{"backup":%q}`, made.Name)},
		{"GET", "/api/instances/a/export", ""},
	} {
		if code, _ := p.do(r.method, r.path, r.body); code != 403 {
			t.Errorf("viewer %s %s: %d", r.method, r.path, code)
		}
	}
	p.signIn("owner")

	if code, _ := p.do("DELETE", "/api/backups/..%2Fpanel.db", ""); code != 404 {
		t.Errorf("delete outside: %d", code)
	}
	if code, b := p.do("DELETE", "/api/backups/"+made.Name, ""); code != 200 {
		t.Fatalf("delete: %d %s", code, b)
	}
	if _, b := p.do("GET", "/api/backups", ""); !strings.Contains(string(b), `"backups":[]`) {
		t.Errorf("after delete: %s", b)
	}
	audit, _ := a.Store.AuditLog(context.Background(), "", 0, 50)
	var actions []string
	for _, e := range audit {
		actions = append(actions, e.Action)
	}
	for _, want := range []string{"backup.create", "server.restore", "backup.download", "backup.settings", "backup.delete"} {
		if !strings.Contains(strings.Join(actions, " "), want) {
			t.Errorf("no %s in the audit log: %v", want, actions)
		}
	}
}

// A server exported here comes back as a new one, on free ports.
func TestExportAndImport(t *testing.T) {
	p, a := newBackupPanel(t)
	in, _ := a.Reg.Get("a")
	dir := in.Def().Dir
	writeFile(t, filepath.Join(dir, "ReSkateServer.json"), `{"name":"A","port":27015,"query_port":27016}`)
	writeOwnMod(t, filepath.Join(dir, "Mods", "bbcity"), "1.0.0")
	ann := store.Announcement{Instance: "a", Message: "Join our Discord", Interval: 600, Enabled: true}
	if err := a.Store.SaveAnnouncement(context.Background(), &ann); err != nil {
		t.Fatal(err)
	}

	resp, err := p.client.Get(p.url + "/api/instances/a/export")
	if err != nil {
		t.Fatal(err)
	}
	var zipped bytes.Buffer
	zipped.ReadFrom(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Disposition"), "a-export-") {
		t.Fatalf("export: %d %s", resp.StatusCode, zipped.String())
	}

	code, b := p.uploadTo("/api/imports", "a-export.zip", zipped.Bytes(), 1000)
	var res struct {
		ID    string
		Notes []string
	}
	if st := p.runJob(code, []byte(b), &res); st.Phase != "done" || res.ID != "a-2" {
		t.Fatalf("import: %+v %+v", st, res)
	}
	imported, ok := a.Reg.Get("a-2")
	if !ok {
		t.Fatal("the imported server is not in the panel")
	}
	d := imported.Def()
	if d.Dir != filepath.Join(a.ServersDir, "a-2") || !exists(filepath.Join(d.Dir, "Mods", "bbcity", "manifest.json")) {
		t.Errorf("imported into %s", d.Dir)
	}
	if cfg := readFile(d.ConfigPath()); strings.Contains(cfg, "27015") {
		t.Errorf("kept the taken ports: %s", cfg)
	}
	if !strings.Contains(strings.Join(res.Notes, " "), "ports") {
		t.Errorf("notes: %v", res.Notes)
	}
	if list, _ := a.Store.Announcements(context.Background(), "a-2"); len(list) != 1 || list[0].Instance != "a-2" {
		t.Errorf("announcements: %+v", list)
	}
	if defs, _ := a.Store.Instances(context.Background()); len(defs) != 2 {
		t.Errorf("stored servers: %+v", defs)
	}

	// Not a backup at all.
	code, b = p.uploadTo("/api/imports", "x.zip", zipOf("readme.txt", "hi"), 1000)
	if code != 400 || !strings.Contains(b, "backup.json") {
		t.Errorf("import of another zip: %d %s", code, b)
	}
}

// Only owners see or change a server's Steam login token. Others aren't sent
// it, can't set it, and their exports, restores and imports leave it be.
func TestSteamTokenIsOwnerOnly(t *testing.T) {
	p, a := newBackupPanel(t)
	in, _ := a.Reg.Get("a")
	cfg := in.Def().ConfigPath()
	writeFile(t, cfg, `{"name":"A","steam_token":"OLDTOKEN","admins":["76561198000000009"]}`)
	code, b := p.do("POST", "/api/backups", `{}`)
	var made backup.Info
	if st := p.runJob(code, b, &made); st.Phase != "done" {
		t.Fatalf("backup: %+v", st)
	}
	writeFile(t, cfg, `{"name":"A","steam_token":"TOKEN","admins":["76561198000000001"]}`)
	if _, b := p.do("GET", "/api/instances/a/settings", ""); !strings.Contains(string(b), `"steam_token":"TOKEN"`) {
		t.Errorf("an owner's settings: %s", b)
	}
	exported := p.export("a")

	code, b = p.do("POST", "/api/roles", `{"name":"Manager","permissions":["settings.view","settings.edit","instances.manage"]}`)
	var role auth.Role
	if json.Unmarshal(b, &role); code != 200 {
		t.Fatalf("create role: %d %s", code, b)
	}
	p.createUser("manager", grant(role.ID))
	p.signIn("manager")
	for _, path := range []string{"/api/meta", "/api/instances/a/settings"} {
		if _, b := p.do("GET", path, ""); strings.Contains(string(b), "steam_token") || strings.Contains(string(b), "TOKEN") {
			t.Errorf("%s shows the token: %s", path, b)
		}
	}
	if code, b := p.do("POST", "/api/instances/a/settings", `{"values":{"steam_token":"MINE"}}`); code != http.StatusForbidden {
		t.Errorf("set the token: %d %s", code, b)
	}
	if code, b := p.do("POST", "/api/instances/a/settings", `{"values":{"name":"B"}}`); code != 200 || !strings.Contains(readFile(cfg), `"TOKEN"`) {
		t.Errorf("save another setting: %d %s, %s", code, b, readFile(cfg))
	}

	// Nor bring back in-game admins they couldn't add by hand.
	code, b = p.do("POST", "/api/instances/a/restore", fmt.Sprintf(`{"backup":%q}`, made.Name))
	if st := p.runJob(code, b, nil); st.Phase != "done" || !strings.Contains(readFile(cfg), `"TOKEN"`) || !strings.Contains(readFile(cfg), `"A"`) ||
		!strings.Contains(readFile(cfg), "76561198000000001") || strings.Contains(readFile(cfg), "76561198000000009") {
		t.Errorf("restore: %+v, %s", st, readFile(cfg))
	}

	if got := zipFile(t, p.export("a"), "servers/a/ReSkateServer.json"); got == "" || strings.Contains(got, "TOKEN") {
		t.Errorf("a manager's export holds %s", got)
	}
	code, s := p.uploadTo("/api/imports", "a-export.zip", exported, 1000)
	var res struct{ ID string }
	if st := p.runJob(code, []byte(s), &res); st.Phase != "done" {
		t.Fatalf("import: %+v", st)
	}
	imported, _ := a.Reg.Get(res.ID)
	if got := readFile(imported.Def().ConfigPath()); got == "" || strings.Contains(got, "TOKEN") || strings.Contains(got, "76561198000000001") {
		t.Errorf("a manager's import of an owner's export: %s", got)
	}
}

func (p *panel) export(id string) []byte {
	p.t.Helper()
	resp, err := p.client.Get(p.url + "/api/instances/" + id + "/export?mods=0")
	if err != nil {
		p.t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	buf.ReadFrom(resp.Body)
	if resp.StatusCode != 200 {
		p.t.Fatalf("export: %d %s", resp.StatusCode, buf.String())
	}
	return buf.Bytes()
}

func zipFile(t *testing.T, zipped []byte, name string) string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(zipped), int64(len(zipped)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		if f.Name == name {
			r, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			b, _ := io.ReadAll(r)
			return string(b)
		}
	}
	return ""
}
