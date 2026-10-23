package parks

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/instance"
	"github.com/xThrasherrr/ReSkateManager/internal/logparse"
	"github.com/xThrasherrr/ReSkateManager/internal/serverconfig"
	"github.com/xThrasherrr/ReSkateManager/internal/store"
)

var setup = []serverconfig.ParkMod{
	{Name: "street", Folder: "street", Map: "San Vansterdam"},
	{Name: "plaza", Folder: "plaza", Map: "San Vansterdam", Key: "bam"},
	{Name: "bowl", Folder: "bowl", Map: "Isle of Grom"},
}

func TestCheck(t *testing.T) {
	ok := Settings{Pools: []Pool{{Name: " Street set ", Map: "san vansterdam", Parks: []string{"street", "plaza"}}}, Chat: Chat{Roles: []string{"builder"}, Minutes: 30}}
	if err := check(ok, setup, 3); err != nil || ok.Pools[0].Name != "Street set" {
		t.Fatalf("%v, name %q", err, ok.Pools[0].Name)
	}
	pool := func(name, m string, parks ...string) Settings {
		return Settings{Pools: []Pool{{Name: name, Map: m, Parks: parks}}}
	}
	for _, c := range []struct {
		name string
		set  Settings
		want string
	}{
		{"no name", pool(" ", "San Vansterdam", "street"), "name"},
		{"no parks", pool("a", "San Vansterdam"), "no parks"},
		{"over the limit", pool("a", "San Vansterdam", "street", "plaza", "bowl", "x"), "at most 3"},
		{"not set up", pool("a", "San Vansterdam", "gone"), "isn't set up"},
		{"another map", pool("a", "San Vansterdam", "street", "bowl"), "is for Isle of Grom"},
		{"twice", pool("a", "San Vansterdam", "street", "street"), "twice"},
		{"same names", Settings{Pools: []Pool{{Name: "A", Map: "Isle of Grom", Parks: []string{"bowl"}}, {Name: "a ", Map: "Isle of Grom", Parks: []string{"bowl"}}}}, "two pools"},
		{"minutes", Settings{Chat: Chat{Minutes: 1441}}, "minutes"},
		{"role", Settings{Chat: Chat{Roles: []string{"Bad Role"}}}, "role's name"},
	} {
		if err := check(c.set, setup, 3); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}

const bob, zee = "76561198000000002", "76561198000000003"

// A pool goes up as the server starts on its map, and a player whose role
// may use /parks spawns a set-up park with it, for the minutes set; nobody
// else does, and nothing they type but a park's name reaches the console.
func TestPoolAndChat(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "data"), 0o755)
	os.WriteFile(filepath.Join(dir, "ReSkateServer.json"), []byte(`{"maps": {"park_mods": [
		{"name": "street", "folder": "street", "map": "San Vansterdam", "key": ""},
		{"name": "plaza", "folder": "plaza", "map": "San Vansterdam", "key": "bam"}]},
		"commands": [{"name": "parks", "reply": "", "command": ["status plugin:manager:parks"], "admin": false}]}`), 0o644)
	os.WriteFile(filepath.Join(dir, "data", "roles.json"), []byte(`{"default_role": "player", "roles": [
		{"name": "player", "tag": "", "color": "#FFFFFF", "rank": 0, "commands": [], "permissions": ["chat"]},
		{"name": "builder", "tag": "B", "color": "#FFFFFF", "rank": 10, "commands": [], "permissions": []}],
		"players": {"`+bob+`": "builder"}}`), 0o644)

	db, err := store.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	set := Settings{Pools: []Pool{{Name: "street set", Map: "San Vansterdam", Parks: []string{"street", "plaza"}}}, Chat: Chat{Roles: []string{"builder"}, Minutes: 30}}
	if err := Save(ctx, db, "s1", set, setup, 3); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := &Service{Store: db, Log: log}
	if w := svc.Words("s1"); len(w) != 1 || w[0] != (serverconfig.Word{Name: Word, Plugin: Source}) {
		t.Errorf("words %v", w)
	}

	reg := instance.NewRegistry(log)
	reg.Wire = func(in *instance.Instance) { in.OnNotice = svc.Notice(in) }
	in := reg.Add(instance.Def{ID: "s1", Name: "S1", Dir: dir})
	if out, err := exec.Command("go", "build", "-o", in.Def().Exe(), "../instance/testdata/fakeserver").CombinedOutput(); err != nil {
		t.Fatalf("build fake server: %v: %s", err, out)
	}
	if err := in.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		in.Stop(ctx)
	})
	typed := func(line string) bool {
		return slices.ContainsFunc(in.Console(), func(e logparse.Entry) bool { return e.Text == line })
	}
	waitFor(t, func() bool { return typed("park-mod add street plaza") })

	for _, l := range []string{"join " + bob + " Bob", "join " + zee + " Zee", "typed 1 Zee /parks add street", "typed 1 Bob /parks add street; quit"} {
		if _, err := in.Command(ctx, l, "test"); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, func() bool {
		return typed("msg "+zee+" Your role can't spawn parks here.") && typed("msg "+bob+" No park is called that. Type /parks add <name> or /parks remove <name>; /parks lists them.")
	})
	time.Sleep(commandGap)
	if _, err := in.Command(ctx, "typed 1 Bob /parks add plaza", "test"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return typed("park-mod add plaza 30") && typed("msg "+bob+" add plaza 30") })
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatal("timed out")
}
