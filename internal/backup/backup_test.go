package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/instance"
	"github.com/xThrasherrr/ReSkateManager/internal/serverconfig"
	"github.com/xThrasherrr/ReSkateManager/internal/store"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeMod makes a mod folder with a manifest, a map and a payload.
func writeMod(t *testing.T, dir, version string) {
	t.Helper()
	write(t, filepath.Join(dir, "manifest.json"), `{"name":"`+filepath.Base(dir)+`","version_number":"`+version+`"}`)
	write(t, filepath.Join(dir, "reskate-levels.json"), `{"levels":[{"asset":"Levels/`+filepath.Base(dir)+`","displayName":"`+filepath.Base(dir)+`"}]}`)
	write(t, filepath.Join(dir, "assets", "level.bundle"), strings.Repeat(version, 5000))
}

// tree maps each regular file under dir to a hash of its contents. Links are
// listed, not followed, as "link".
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			t.Fatal(err)
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		switch {
		case d.Type().IsRegular():
			data, _ := os.ReadFile(p)
			sum := sha256.Sum256(data)
			out[rel] = hex.EncodeToString(sum[:8])
		case !d.IsDir():
			out[rel] = "link"
		}
		return nil
	})
	return out
}

func diffTrees(t *testing.T, what string, got, want map[string]string) {
	t.Helper()
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %s is %q, want %q", what, k, got[k], v)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("%s: %s should not be there", what, k)
		}
	}
}

// manager is a manager's folder: a database with servers in it, and the
// shared mods.
type manager struct {
	root, data, servers, shared string
	st                          *store.Store
	reg                         *instance.Registry
	svc                         *Service
}

func newManager(t *testing.T, root string) *manager {
	t.Helper()
	m := &manager{root: root, data: filepath.Join(root, "data"), servers: filepath.Join(root, "servers"), shared: filepath.Join(root, serverconfig.SharedName)}
	if err := os.MkdirAll(m.data, 0o700); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(m.data, "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	m.st = st
	log := slog.New(slog.DiscardHandler)
	m.reg = instance.NewRegistry(log)
	m.svc = &Service{Dir: filepath.Join(root, "backups"), DataDir: m.data, Root: root, SharedDir: m.shared, Version: "test", Store: st, Reg: m.reg, Log: log}
	return m
}

func (m *manager) addServer(t *testing.T, d instance.Def) instance.Def {
	t.Helper()
	if d.Dir == "" {
		d.Dir = filepath.Join(m.servers, d.ID)
	}
	if err := m.st.CreateInstance(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	m.reg.Add(d)
	return d
}

// TestBackUpWipeRestore backs up a manager with mods, deletes everything but
// the backup, restores it and compares what came back with what was there.
func TestBackUpWipeRestore(t *testing.T) {
	ctx := context.Background()
	m := newManager(t, t.TempDir())
	write(t, filepath.Join(m.data, "manager.toml"), "listen = \"127.0.0.1:40999\"\n")
	if err := m.st.Set(ctx, "retention", `{"auditDays":30}`); err != nil {
		t.Fatal(err)
	}
	lobby := m.addServer(t, instance.Def{ID: "lobby", Name: "Lobby", AutoStart: true, SharedMods: serverconfig.SharedAll, RestartTimes: []string{"04:00"}})
	write(t, filepath.Join(lobby.Dir, "ReSkateServer.json"), `{"name":"Lobby","port":27015}`)
	write(t, filepath.Join(lobby.Dir, "world-layers.json"), `{"rows":[]}`)
	write(t, filepath.Join(lobby.Dir, "data", "bans.json"), `[{"id":"76561198000000001","name":"A","added":1}]`) // ReSkate 1.1.7
	write(t, lobby.Exe(), "a server program")
	write(t, filepath.Join(lobby.Dir, "ReSkateServer.log"), "a log")
	writeMod(t, filepath.Join(lobby.Dir, "Mods", "own"), "1.0.0")
	writeMod(t, filepath.Join(lobby.Dir, "DisabledMods", "off"), "2.0.0")
	writeMod(t, filepath.Join(m.shared, "Mods", "common"), "3.0.0")
	if err := serverconfig.SyncShared(lobby.Dir, m.shared, lobby.SharedMods); err != nil {
		t.Fatal(err)
	}
	// One kept outside servers/, as an owner can.
	other := m.addServer(t, instance.Def{ID: "other", Name: "Other", Dir: filepath.Join(m.root, "elsewhere", "other")})
	write(t, filepath.Join(other.Dir, "ReSkateServer.json"), `{"name":"Other"}`)

	info, err := m.svc.Create(ctx, Options{Kind: Manual, By: "owner", Database: true, Servers: []string{"lobby", "other"}, Mods: true})
	if err != nil {
		t.Fatal(err)
	}
	if !info.Database || len(info.Servers) != 2 || !info.SharedFiles || info.By != "owner" {
		t.Fatalf("manifest: %+v", info.Manifest)
	}
	wantLobby := tree(t, lobby.Dir)
	delete(wantLobby, filepath.Base(lobby.Exe())) // downloaded again, never backed up
	delete(wantLobby, "ReSkateServer.log")
	delete(wantLobby, "Mods/common") // the link, made again as the server starts
	wantOther := tree(t, other.Dir)
	wantShared := tree(t, m.shared)
	wantDefs, _ := m.st.Instances(ctx)
	wantToml, _ := os.ReadFile(filepath.Join(m.data, "manager.toml"))

	m.st.Close()
	for _, d := range []string{m.data, m.servers, m.shared, filepath.Join(m.root, "elsewhere")} {
		if err := os.RemoveAll(d); err != nil {
			t.Fatal(err)
		}
	}

	svc := &Service{Dir: m.svc.Dir, DataDir: m.data, Root: m.root, SharedDir: m.shared, Version: "test"}
	rep, err := svc.RestoreAll(ctx, filepath.Join(m.svc.Dir, info.Name))
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(rep.Servers)
	if rep.Safety != "" || len(rep.Moved) != 0 || !slices.Equal(rep.Servers, []string{"Lobby", "Other"}) || !slices.Equal(rep.Shared, []string{"common"}) {
		t.Errorf("report: %+v", rep)
	}
	diffTrees(t, "lobby", tree(t, lobby.Dir), wantLobby)
	diffTrees(t, "other", tree(t, other.Dir), wantOther)
	diffTrees(t, "shared", tree(t, m.shared), wantShared)
	if toml, _ := os.ReadFile(filepath.Join(m.data, "manager.toml")); !bytes.Equal(toml, wantToml) {
		t.Errorf("manager.toml is %q", toml)
	}
	st, err := store.Open(filepath.Join(m.data, "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	defs, _ := st.Instances(ctx)
	if len(defs) != len(wantDefs) {
		t.Fatalf("servers: %+v", defs)
	}
	for _, w := range wantDefs {
		i := slices.IndexFunc(defs, func(d instance.Def) bool { return d.ID == w.ID })
		if i < 0 || defs[i].Dir != w.Dir || defs[i].Name != w.Name || defs[i].SharedMods != w.SharedMods ||
			!slices.Equal(defs[i].RestartTimes, w.RestartTimes) || defs[i].AutoStart != w.AutoStart {
			t.Errorf("server %s did not come back as %+v: %+v", w.ID, w, defs)
		}
	}
	if v, _ := st.Get(ctx, "retention"); v != `{"auditDays":30}` {
		t.Errorf("kv retention is %q", v)
	}
	// As the manager does on start.
	if err := serverconfig.SyncShared(lobby.Dir, m.shared, serverconfig.SharedAll); err != nil {
		t.Fatal(err)
	}
	if !serverconfig.IsShared(lobby.Dir, "common") {
		t.Error("the shared mod was not linked again")
	}
}

// TestRestoreAllSavesTheDatabase backs up the database it replaces, and
// moves servers from the backup's servers folder into this manager's.
func TestRestoreAllSavesTheDatabase(t *testing.T) {
	ctx := context.Background()
	old := newManager(t, t.TempDir())
	lobby := old.addServer(t, instance.Def{ID: "lobby", Name: "Lobby"})
	write(t, filepath.Join(lobby.Dir, "ReSkateServer.json"), `{"name":"Lobby"}`)
	info, err := old.svc.Create(ctx, Options{Kind: Manual, Database: true, Servers: []string{"lobby"}})
	if err != nil {
		t.Fatal(err)
	}

	// Another manager, in another folder, with a server of its own.
	m := newManager(t, t.TempDir())
	m.addServer(t, instance.Def{ID: "mine", Name: "Mine"})
	m.st.Close()
	svc := &Service{Dir: m.svc.Dir, DataDir: m.data, Root: m.root, SharedDir: m.shared, Version: "test"}
	rep, err := svc.RestoreAll(ctx, filepath.Join(old.svc.Dir, info.Name))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Safety == "" || !slices.Equal(rep.Moved, []string{"Lobby"}) {
		t.Fatalf("report: %+v", rep)
	}
	moved := filepath.Join(m.servers, "lobby", "ReSkateServer.json")
	if data, _ := os.ReadFile(moved); string(data) != `{"name":"Lobby"}` {
		t.Errorf("%s holds %q", moved, data)
	}
	st, err := store.Open(filepath.Join(m.data, "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if defs, _ := st.Instances(ctx); len(defs) != 1 || defs[0].Dir != filepath.Join(m.servers, "lobby") {
		t.Errorf("servers after: %+v", defs)
	}
	// The database it replaced, with "mine", is in the safety backup.
	r, err := OpenFile(filepath.Join(m.svc.Dir, rep.Safety))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.Kind != Restore || !r.Database {
		t.Errorf("safety backup: %+v", r.Manifest)
	}
}

// TestRestoreMods swaps a server's own mods for the backup's, and leaves its
// links be unless one is in the way.
func TestRestoreMods(t *testing.T) {
	ctx := context.Background()
	m := newManager(t, t.TempDir())
	d := m.addServer(t, instance.Def{ID: "a", Name: "A", SharedMods: serverconfig.SharedAll})
	writeMod(t, filepath.Join(d.Dir, "Mods", "keep"), "1.0.0")
	writeMod(t, filepath.Join(d.Dir, "DisabledMods", "moved"), "1.0.0")
	writeMod(t, filepath.Join(m.shared, "Mods", "linked"), "9.0.0")
	writeMod(t, filepath.Join(m.shared, "Mods", "clash"), "9.0.0")
	info, err := m.svc.Create(ctx, Options{Kind: Manual, Servers: []string{"a"}, Mods: true})
	if err != nil {
		t.Fatal(err)
	}
	want := tree(t, d.Dir)

	// Since then "keep" changed, "moved" was enabled, a mod was added, and
	// the shared mods were linked.
	writeMod(t, filepath.Join(d.Dir, "Mods", "keep"), "1.1.0")
	if err := serverconfig.SetModEnabled(d.Dir, "moved", true); err != nil {
		t.Fatal(err)
	}
	writeMod(t, filepath.Join(d.Dir, "Mods", "added"), "1.0.0")
	if err := serverconfig.SyncShared(d.Dir, m.shared, serverconfig.SharedAll); err != nil {
		t.Fatal(err)
	}

	r, err := OpenFile(filepath.Join(m.svc.Dir, info.Name))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	n, err := r.RestoreMods(ctx, "a", d.Dir, nil)
	if err != nil || n != 2 {
		t.Fatalf("restored %d mods: %v", n, err)
	}
	want["Mods/linked"], want["Mods/clash"] = "link", "link" // left as they were
	diffTrees(t, "after restore", tree(t, d.Dir), want)
	if leftovers, _ := filepath.Glob(filepath.Join(d.Dir, ".restore-*")); len(leftovers) > 0 {
		t.Errorf("left %v", leftovers)
	}

	// A backup from when the server had its own "clash" puts it back in place of the link.
	if err := os.Remove(filepath.Join(d.Dir, "Mods", "clash")); err != nil {
		t.Fatal(err)
	}
	writeMod(t, filepath.Join(d.Dir, "Mods", "clash"), "5.0.0")
	own, err := m.svc.Create(ctx, Options{Kind: Manual, Servers: []string{"a"}, Mods: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(d.Dir, "Mods", "clash")); err != nil {
		t.Fatal(err)
	}
	if err := serverconfig.SyncShared(d.Dir, m.shared, serverconfig.SharedAll); err != nil {
		t.Fatal(err)
	}
	if !serverconfig.IsShared(d.Dir, "clash") {
		t.Fatal("clash should be linked by now")
	}
	r2, err := OpenFile(filepath.Join(m.svc.Dir, own.Name))
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()
	if _, err := r2.RestoreMods(ctx, "a", d.Dir, nil); err != nil {
		t.Fatal(err)
	}
	if serverconfig.IsShared(d.Dir, "clash") || !serverconfig.IsShared(d.Dir, "linked") {
		t.Error("clash should be the server's own again, and linked still a link")
	}
	if data, _ := os.ReadFile(filepath.Join(d.Dir, "Mods", "clash", "manifest.json")); !strings.Contains(string(data), "5.0.0") {
		t.Errorf("the server's clash holds %s", data)
	}
	if data, _ := os.ReadFile(filepath.Join(m.shared, "Mods", "clash", "manifest.json")); !strings.Contains(string(data), "9.0.0") {
		t.Error("the shared copy changed")
	}
}

// TestExportImport moves a server, with the shared mods it loads, to a new
// folder.
func TestExportImport(t *testing.T) {
	ctx := context.Background()
	m := newManager(t, t.TempDir())
	d := m.addServer(t, instance.Def{ID: "a", Name: "Park", AutoUpdate: true, SharedMods: serverconfig.SharedPick, RestartHours: 6})
	write(t, filepath.Join(d.Dir, "ReSkateServer.json"), `{"name":"Park","port":27015}`)
	write(t, d.Exe(), "program")
	writeMod(t, filepath.Join(d.Dir, "Mods", "own"), "1.0.0")
	writeMod(t, filepath.Join(m.shared, "Mods", "common"), "3.0.0")
	if err := serverconfig.SyncShared(d.Dir, m.shared, serverconfig.SharedPick); err != nil {
		t.Fatal(err)
	}
	ann := []store.Announcement{{Instance: "a", Message: "Join our Discord", Interval: 600, Enabled: true}}

	var buf bytes.Buffer
	if err := WriteExport(ctx, &buf, d, true, false, ann, "test"); err != nil {
		t.Fatal(err)
	}
	zipPath := filepath.Join(t.TempDir(), "park.zip")
	write(t, zipPath, buf.String())
	r, err := OpenFile(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	dir := filepath.Join(t.TempDir(), "servers", "park")
	srv, err := r.ImportServer(ctx, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if srv.Name != "Park" || !srv.AutoUpdate || srv.SharedMods != serverconfig.SharedPick || srv.RestartHours != 6 || len(srv.Announcements) != 1 {
		t.Errorf("imported server: %+v", srv)
	}
	got := tree(t, dir)
	want := map[string]string{}
	for k, v := range tree(t, d.Dir) {
		if k != filepath.Base(d.Exe()) && !strings.HasPrefix(k, "DisabledMods/common") {
			want[k] = v
		}
	}
	// The shared mod, disabled there as SharedPick leaves a new one, comes as the server's own.
	for k, v := range tree(t, filepath.Join(m.shared, "Mods", "common")) {
		want["DisabledMods/common/"+k] = v
	}
	diffTrees(t, "imported", got, want)
	if _, err := r.ImportServer(ctx, dir, nil); err == nil {
		t.Error("imported over a folder that exists")
	}
}

// fakeBackup writes a backup with manifest m straight into dir.
func fakeBackup(t *testing.T, dir, name string, m Manifest) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m.Format = Format
	if err := writeZip(context.Background(), f, m, nil, &progress{}); err != nil {
		t.Fatal(err)
	}
}

func TestPrune(t *testing.T) {
	dir := t.TempDir()
	s := &Service{Dir: dir}
	day := time.Date(2026, 10, 1, 4, 0, 0, 0, time.UTC).Unix()
	srv := func(id string) []Server { return []Server{{ID: id}} }
	for i := range 5 {
		at := day + int64(i)*86400
		stamp := time.Unix(at, 0).UTC().Format("20060102-150405")
		fakeBackup(t, dir, "backup-"+stamp+"-scheduled.zip", Manifest{Kind: Scheduled, Created: at})
		fakeBackup(t, dir, "backup-"+stamp+"-manual.zip", Manifest{Kind: Manual, Created: at})
		fakeBackup(t, dir, "backup-"+stamp+"-server-update-a.zip", Manifest{Kind: ServerUpdate, Created: at, Servers: srv("a")})
		fakeBackup(t, dir, "backup-"+stamp+"-server-update-b.zip", Manifest{Kind: ServerUpdate, Created: at, Servers: srv("b")})
	}
	// One file time for all, as a file system with coarse times gives
	// backups made in the same instant.
	same := time.Now().Add(-time.Hour)
	names, _ := filepath.Glob(filepath.Join(dir, "backup-*.zip"))
	for _, n := range names {
		os.Chtimes(n, same, same)
	}
	write(t, filepath.Join(dir, "backup-20261001-000000-broken.zip"), "not a zip")
	write(t, filepath.Join(dir, ".partial-old.zip"), "cut short")
	os.Chtimes(filepath.Join(dir, ".partial-old.zip"), time.Now().Add(-48*time.Hour), time.Now().Add(-48*time.Hour))
	write(t, filepath.Join(dir, ".partial-new.zip"), "being written")

	gone, err := s.Prune(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(gone) != 9 {
		t.Errorf("deleted %d: %v", len(gone), gone)
	}
	list, _ := s.List()
	count := map[string]int{}
	for _, b := range list {
		key := string(b.Kind)
		if b.Kind == ServerUpdate {
			key += "/" + b.Servers[0].ID
		}
		if b.Error != "" {
			key = "broken"
		}
		count[key]++
	}
	want := map[string]int{"scheduled": 2, "manual": 5, "server-update/a": 2, "server-update/b": 2, "broken": 1}
	if !maps.Equal(count, want) {
		t.Errorf("kept %v, want %v", count, want)
	}
	// The newest of each are kept, newest first.
	var kept []int64
	for _, b := range list {
		if b.Kind == Scheduled {
			kept = append(kept, b.Created)
		}
	}
	if !slices.Equal(kept, []int64{day + 4*86400, day + 3*86400}) {
		t.Errorf("kept the scheduled backups of %v, want the last two days", kept)
	}
	if _, err := os.Stat(filepath.Join(dir, ".partial-old.zip")); err == nil {
		t.Error("an old partial backup was kept")
	}
	if _, err := os.Stat(filepath.Join(dir, ".partial-new.zip")); err != nil {
		t.Error("a backup being written was deleted")
	}
}

// TestUnsafeBackups refuses zips that aren't backups, or that would write
// outside where they go.
func TestUnsafeBackups(t *testing.T) {
	dir := t.TempDir()
	zipOf := func(name string, files map[string]string) string {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for _, n := range slices.Sorted(maps.Keys(files)) {
			w, _ := zw.Create(n)
			w.Write([]byte(files[n]))
		}
		zw.Close()
		p := filepath.Join(dir, name)
		write(t, p, buf.String())
		return p
	}
	manifest := `{"format":1,"kind":"manual","servers":[{"id":"a","name":"A","modFiles":true}]}`
	for name, files := range map[string]map[string]string{
		"no-manifest.zip": {"servers/a/ReSkateServer.json": "{}"},
		"newer.zip":       {"backup.json": `{"format":99,"servers":[]}`},
		"bad-id.zip":      {"backup.json": `{"format":1,"servers":[{"id":"../x"}]}`},
	} {
		if r, err := OpenFile(zipOf(name, files)); err == nil {
			r.Close()
			t.Errorf("%s opened", name)
		}
	}
	for name, files := range map[string]map[string]string{
		"dotdot.zip":    {"backup.json": manifest, "servers/a/Mods/m/../../../../evil.txt": "x"},
		"backslash.zip": {"backup.json": manifest, `servers/a/Mods/m/..\..\..\evil.txt`: "x"},
	} {
		r, err := OpenFile(zipOf(name, files))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := r.RestoreMods(context.Background(), "a", filepath.Join(dir, "out-"+name), nil); err == nil {
			t.Errorf("%s restored", name)
		}
		if _, err := r.ImportServer(context.Background(), filepath.Join(dir, "in-"+name), nil); err == nil {
			t.Errorf("%s imported", name)
		}
		r.Close()
	}
	if _, err := os.Stat(filepath.Join(dir, "evil.txt")); err == nil {
		t.Error("a file landed outside")
	}
	// Files outside the known places are left out.
	r, err := OpenFile(zipOf("extra.zip", map[string]string{"backup.json": manifest, "servers/a/ReSkateServer.exe": "program",
		"servers/a/ReSkateServer.json": "{}", "servers/a/Mods/m/x.txt": "x", "servers/a/Mods/loose.txt": "x"}))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	only := func(what string, got map[string]string) {
		if len(got) != 2 || got["ReSkateServer.json"] == "" || got["Mods/m/x.txt"] == "" {
			t.Errorf("%s %v", what, got)
		}
	}
	out := filepath.Join(dir, "extra")
	if _, err := r.RestoreConfig("a", out); err != nil {
		t.Fatal(err)
	}
	if _, err := r.RestoreMods(context.Background(), "a", out, nil); err != nil {
		t.Fatal(err)
	}
	only("restored", tree(t, out))
	in := filepath.Join(dir, "imported")
	if _, err := r.ImportServer(context.Background(), in, nil); err != nil {
		t.Fatal(err)
	}
	only("imported", tree(t, in))
}

func TestBeforeMigration(t *testing.T) {
	m := newManager(t, t.TempDir())
	if err := m.svc.BeforeMigration(m.st.DB, 3); err != nil {
		t.Fatal(err)
	}
	list, _ := m.svc.List()
	if len(list) != 1 || list[0].Kind != Migration || !list[0].Database || len(list[0].Servers) != 0 {
		t.Fatalf("backups: %+v", list)
	}
}

func TestDue(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	daily := Settings{Every: 24, Keep: 7}
	for _, c := range []struct {
		name         string
		set          Settings
		last, failed time.Time
		want         bool
	}{
		{"never made", daily, time.Time{}, time.Time{}, true},
		{"made today", daily, now.Add(-23 * time.Hour), time.Time{}, false},
		{"a day ago", daily, now.Add(-24 * time.Hour), time.Time{}, true},
		{"off", Settings{Every: 0}, time.Time{}, time.Time{}, false},
		{"failed just now", daily, time.Time{}, now.Add(-10 * time.Minute), false},
		{"failed an hour ago", daily, time.Time{}, now.Add(-time.Hour), true},
	} {
		if got := due(c.set, c.last, c.failed, now); got != c.want {
			t.Errorf("%s: due is %v", c.name, got)
		}
	}
}

func TestMovedDir(t *testing.T) {
	to := filepath.Join(string(filepath.Separator)+"data", "rsm")
	for _, c := range []struct {
		dir, from string
		want      string
	}{
		{`G:\ReskateManager\servers\lobby`, `G:\ReskateManager`, filepath.Join(to, "servers", "lobby")},
		{`g:\reskatemanager\Servers\lobby`, `G:\ReskateManager`, filepath.Join(to, "servers", "lobby")},
		{"/opt/rsm/servers/a/b", "/opt/rsm", filepath.Join(to, "servers", "a", "b")},
		{`D:\Elsewhere\lobby`, `G:\ReskateManager`, ""},
		{"/opt/rsm/servers/../etc", "/opt/rsm", ""},
		{"/opt/rsm/serversX/a", "/opt/rsm", ""},
	} {
		got, ok := movedDir(c.dir, c.from, to)
		if !ok {
			got = ""
		}
		if got != c.want {
			t.Errorf("movedDir(%q, %q) = %q, want %q", c.dir, c.from, got, c.want)
		}
	}
}

// A restored database starts with no sessions: some it held may have been
// ended since the backup was made.
func TestRestoreAllEndsSessions(t *testing.T) {
	ctx := context.Background()
	old := newManager(t, t.TempDir())
	if _, err := old.st.DB.Exec(`INSERT INTO users(username, created_at) VALUES('owner', 0);
		INSERT INTO sessions(token_hash, user_id, created_at, expires_at, last_seen_at) VALUES('h', 1, 0, 9999999999, 0)`); err != nil {
		t.Fatal(err)
	}
	info, err := old.svc.Create(ctx, Options{Kind: Manual, Database: true})
	if err != nil {
		t.Fatal(err)
	}
	m := newManager(t, t.TempDir())
	m.st.Close()
	svc := &Service{Dir: m.svc.Dir, DataDir: m.data, Root: m.root, SharedDir: m.shared, Version: "test"}
	if _, err := svc.RestoreAll(ctx, filepath.Join(old.svc.Dir, info.Name)); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(m.data, "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var users, sessions int
	st.DB.QueryRow("SELECT COUNT(*) FROM users").Scan(&users)
	st.DB.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&sessions)
	if users != 1 || sessions != 0 {
		t.Errorf("%d users, %d sessions after the restore", users, sessions)
	}
}

// A backup from a newer manager is refused before anything is replaced.
func TestRestoreAllRefusesNewerSchema(t *testing.T) {
	ctx := context.Background()
	old := newManager(t, t.TempDir())
	latest, _ := store.Latest()
	if _, err := old.st.DB.Exec(fmt.Sprintf("PRAGMA user_version = %d", latest+1)); err != nil {
		t.Fatal(err)
	}
	info, err := old.svc.Create(ctx, Options{Kind: Manual, Database: true})
	if err != nil {
		t.Fatal(err)
	}
	m := newManager(t, t.TempDir())
	m.addServer(t, instance.Def{ID: "mine", Name: "Mine"})
	m.st.Close()
	svc := &Service{Dir: m.svc.Dir, DataDir: m.data, Root: m.root, SharedDir: m.shared, Version: "test"}
	if _, err := svc.RestoreAll(ctx, filepath.Join(old.svc.Dir, info.Name)); err == nil {
		t.Fatal("restored a newer schema")
	}
	st, err := store.Open(filepath.Join(m.data, "manager.db"))
	if err != nil {
		t.Fatalf("the database was replaced: %v", err)
	}
	defer st.Close()
	if defs, _ := st.Instances(ctx); len(defs) != 1 || defs[0].ID != "mine" {
		t.Errorf("servers after: %+v", defs)
	}
}
