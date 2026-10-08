package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/backup"
	"github.com/xThrasherrr/ReSkateManager/internal/host"
	"github.com/xThrasherrr/ReSkateManager/internal/instance"
	"github.com/xThrasherrr/ReSkateManager/internal/serverconfig"
	"github.com/xThrasherrr/ReSkateManager/internal/store"
)

// The host page splits the servers' share of each bucket by the machine's
// samples, and only callers with host.view get it.
func TestHostPerformance(t *testing.T) {
	var st *store.Store
	reg := instance.NewRegistry(slog.New(slog.DiscardHandler))
	reg.Add(instance.Def{ID: "a", Name: "Alpha", Dir: t.TempDir()})
	reg.Add(instance.Def{ID: "b", Name: "Bravo", Dir: t.TempDir()})
	p := newPanel(t, func(a *API) { st, a.Reg = a.Store, reg })
	p.post("/api/auth/login", `{"username":"owner","password":"`+testPassword+`"}`)

	ctx := context.Background()
	// Two ticks in one 30 s bucket of the last hour: a ran for both, b for one.
	t0 := time.Now().Add(-10*time.Minute).Unix() / 30 * 30
	for i, at := range []int64{t0, t0 + 15} {
		h := store.HostSample{At: at, CPU: 50, MemTotal: 8 << 30, MemUsed: 4 << 30, ServersCPU: 30, ServersMem: 3 << 30, Running: 1 + i, Servers: 2, Players: 4}
		if err := st.AddHostSample(ctx, h); err != nil {
			t.Fatal(err)
		}
		if err := st.AddPerfSample(ctx, "a", store.PerfSample{At: at, Run: t0, CPU: 20, Mem: 2 << 30}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.AddPerfSample(ctx, "b", store.PerfSample{At: t0 + 15, Run: t0, CPU: 10, Mem: 1 << 30}); err != nil {
		t.Fatal(err)
	}

	code, b := p.do("GET", "/api/host?range=1h", "")
	if code != http.StatusOK {
		t.Fatalf("host: %d %s", code, b)
	}
	var got struct {
		Points []struct {
			At      int64
			CPU     float64
			Running int
		}
		Servers []hostServer
		Latest  *store.HostSample
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Points) != 1 || got.Points[0].Running != 2 || got.Latest == nil || got.Latest.At != t0+15 {
		t.Errorf("points %+v latest %+v", got.Points, got.Latest)
	}
	if len(got.Servers) != 2 || got.Servers[0].Name != "Alpha" || got.Servers[1].Name != "Bravo" {
		t.Fatalf("servers %+v", got.Servers)
	}
	a, bv := got.Servers[0], got.Servers[1]
	if len(a.Points) != 1 || a.Points[0].CPU != 20 || a.Points[0].Mem != 2<<30 || a.CPU != 20 || a.MemMax != 2<<30 {
		t.Errorf("a %+v", a)
	}
	// b ran for half the bucket: half its reading in the stack, all of it in its average.
	if len(bv.Points) != 1 || bv.Points[0].CPU != 5 || bv.Points[0].Mem != 1<<29 || bv.CPU != 10 || bv.Mem != 1<<30 {
		t.Errorf("b %+v", bv)
	}

	// A Viewer on every server doesn't see the machine; a role with host.view does.
	p.createUser("viewer", grant(p.roleID("Viewer")))
	code, b = p.do("POST", "/api/roles", `{"name":"Host","permissions":["host.view"]}`)
	if code != http.StatusOK {
		t.Fatalf("create role: %d %s", code, b)
	}
	p.createUser("watcher", grant(p.roleID("Host")))
	p.signIn("viewer")
	if code, _ := p.do("GET", "/api/host", ""); code != http.StatusForbidden {
		t.Errorf("viewer: %d, want 403", code)
	}
	p.signIn("watcher")
	if code, _ := p.do("GET", "/api/host", ""); code != http.StatusOK {
		t.Errorf("watcher: %d, want 200", code)
	}
}

// The Host page's disk part: each kind of thing the manager keeps, measured in
// the background, with a shared mod counted once however many servers link it.
func TestHostDisk(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "servers", "a")
	shared := filepath.Join(root, "shared")
	data := filepath.Join(root, "data")
	size := func(path string, n int) { writeFile(t, path, strings.Repeat("x", n)) }
	size(filepath.Join(dir, "ReSkateServer.exe"), 1000)
	size(filepath.Join(dir, "ReSkateServer.json"), 10)
	size(filepath.Join(dir, "ReSkateServer.log"), 300)
	size(filepath.Join(dir, "ReSkateServer.log.1"), 200)
	size(filepath.Join(dir, "console.log"), 50)
	size(filepath.Join(dir, "Mods", "own", "map.bin"), 400)
	size(filepath.Join(dir, "DisabledMods", "old", "map.bin"), 40)
	size(filepath.Join(shared, "Mods", "bbcity", "map.bin"), 3000)
	if err := serverconfig.SyncShared(dir, shared, serverconfig.SharedAll); err != nil {
		t.Fatal(err)
	}
	if !isLink(filepath.Join(dir, "Mods", "bbcity")) {
		t.Fatal("the shared mod isn't linked")
	}
	size(filepath.Join(data, "manager.db"), 700)
	size(filepath.Join(data, ManagerLog), 60)
	size(filepath.Join(data, ManagerLog+".1"), 40)
	size(filepath.Join(root, "backups", "backup.zip"), 5000)

	reg := instance.NewRegistry(slog.New(slog.DiscardHandler))
	reg.Add(instance.Def{ID: "a", Name: "Alpha", Dir: dir})
	p := newPanel(t, func(a *API) {
		a.Reg, a.ServersDir, a.SharedDir, a.DataDir, a.CacheDir = reg, filepath.Dir(dir), shared, data, filepath.Join(root, "cache")
		a.Backups = &backup.Service{Dir: filepath.Join(root, "backups")}
	})
	p.post("/api/auth/login", `{"username":"owner","password":"`+testPassword+`"}`)

	type answer struct {
		At        int64
		Measuring bool
		Drives    []host.Space
		Parts     []diskPart
		Servers   []diskServer
		LowFree   int64
	}
	get := func(query string) answer {
		t.Helper()
		code, b := p.do("GET", "/api/host/disk"+query, "")
		var got answer
		if err := json.Unmarshal(b, &got); code != http.StatusOK || err != nil {
			t.Fatalf("disk: %d %s", code, b)
		}
		return got
	}
	got := get("")
	if !got.Measuring || got.At != 0 || len(got.Parts) != 0 {
		t.Fatalf("first look didn't start measuring: %+v", got)
	}
	if len(got.Drives) != 1 || got.Drives[0].Volume == "" || got.Drives[0].Total == 0 || got.LowFree != 10<<30 {
		t.Fatalf("drives %+v", got.Drives)
	}
	for i := 0; got.Measuring; i++ {
		if i == 500 {
			t.Fatal("still measuring")
		}
		time.Sleep(10 * time.Millisecond)
		got = get("")
	}
	vol := got.Drives[0].Volume
	sizes := map[string]int64{}
	for _, part := range got.Parts {
		if part.Volume != vol {
			t.Errorf("%s on %q, want %q", part.Kind, part.Volume, vol)
		}
		sizes[part.Kind] = part.Size
	}
	want := map[string]int64{"servers": 1450, "logs": 650, "shared": 3000, "backups": 5000, "cache": 0, "data": 700}
	if !maps.Equal(sizes, want) {
		t.Errorf("parts %v, want %v", sizes, want)
	}
	if len(got.Servers) != 1 || got.Servers[0] != (diskServer{ID: "a", Name: "Alpha", Volume: vol, Files: 1010, Mods: 440, Logs: 550}) {
		t.Errorf("servers %+v", got.Servers)
	}
	// Asking again so soon measures nothing new.
	if again := get("?fresh"); again.Measuring || again.At != got.At {
		t.Errorf("measured again at once: %+v", again)
	}

	p.createUser("viewer", grant(p.roleID("Viewer")))
	p.signIn("viewer")
	if code, _ := p.do("GET", "/api/host/disk", ""); code != http.StatusForbidden {
		t.Errorf("viewer: %d, want 403", code)
	}
}
