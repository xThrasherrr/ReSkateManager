package adminsync

import (
	"context"
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/auth"
	"github.com/xThrasherrr/ReSkateManager/internal/instance"
	"github.com/xThrasherrr/ReSkateManager/internal/serverconfig"
	"github.com/xThrasherrr/ReSkateManager/internal/store"
)

const (
	manual = "76561198000000009"
	alice  = "76561198000000001"
	bob    = "76561198000000002"
	slow   = "76561198000000099" // the fake server answers a second late
)

func TestPlan(t *testing.T) {
	for _, c := range []struct {
		what                   string
		current, want, managed []string
		add, remove, next      []string
	}{
		{"new panel admin", []string{manual}, []string{alice}, nil, []string{alice}, nil, []string{alice}},
		{"already in step", []string{manual, alice}, []string{alice}, []string{alice}, nil, nil, []string{alice}},
		{"panel admin loses the permission", []string{manual, alice}, nil, []string{alice}, nil, []string{alice}, []string{}},
		{"a hand-added admin is never removed", []string{manual}, nil, nil, nil, nil, []string{}},
		{"a hand-added admin is not taken over", []string{alice}, []string{alice}, nil, nil, nil, []string{}},
		{"removed by hand, so added back", []string{manual}, []string{alice}, []string{alice}, []string{alice}, nil, []string{alice}},
		{"gone from the server, not wanted", []string{manual}, nil, []string{alice}, nil, nil, []string{}},
	} {
		add, remove, next := Plan(c.current, c.want, c.managed)
		if !slices.Equal(add, c.add) || !slices.Equal(remove, c.remove) || !slices.Equal(next, c.next) {
			t.Errorf("%s: add %v remove %v next %v, want %v %v %v", c.what, add, remove, next, c.add, c.remove, c.next)
		}
	}
}

func admins(t *testing.T, in *instance.Instance) []string {
	t.Helper()
	f, err := serverconfig.Read(in.Def().ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	list := f.Admins()
	slices.Sort(list)
	return list
}

func TestSyncStoppedThenRunning(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(filepath.Join(t.TempDir(), "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	svc, err := auth.New(ctx, st.DB)
	if err != nil {
		t.Fatal(err)
	}
	reg := instance.NewRegistry(log)
	def := instance.Def{ID: "srv", Name: "Srv", Dir: t.TempDir()}
	if out, err := exec.Command("go", "build", "-o", def.Exe(), "../instance/testdata/fakeserver").CombinedOutput(); err != nil {
		t.Fatalf("build fake server: %v\n%s", err, out)
	}
	cfg := &serverconfig.File{Root: map[string]any{"name": "Srv"}}
	cfg.SetAdmins([]string{manual})
	if err := cfg.Write(def.ConfigPath()); err != nil {
		t.Fatal(err)
	}
	in := reg.Add(def)
	s := &Syncer{Store: st, Auth: svc, Reg: reg, Log: log}

	role, err := svc.SaveRole(ctx, auth.Role{Name: "In-game admin", Permissions: []string{auth.IngameAdmin}})
	if err != nil {
		t.Fatal(err)
	}
	grant := func(username, steamID, instanceID string) *auth.User {
		u, err := svc.CreateUser(ctx, auth.NewUser{Username: username, Password: "correct horse battery staple", SteamID: steamID})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.UpdateUser(ctx, u.ID, auth.UserUpdate{Grants: &[]auth.Grant{{RoleID: role.ID, Instance: instanceID}}}); err != nil {
			t.Fatal(err)
		}
		return u
	}
	a := grant("alice", alice, "srv")
	b := grant("bob", bob, "elsewhere")

	// Stopped: the config file is edited.
	if err := s.Sync(ctx, in); err != nil {
		t.Fatal(err)
	}
	if got := admins(t, in); !slices.Equal(got, []string{alice, manual}) {
		t.Fatalf("stopped sync: admins %v", got)
	}

	// Running: the server is sent admin commands and saves them itself.
	if err := in.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		in.Stop(ctx)
	})
	for deadline := time.Now().Add(10 * time.Second); in.State() != instance.Running; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("state %s", in.State())
		}
	}
	disabled := true
	if _, err := svc.UpdateUser(ctx, a.ID, auth.UserUpdate{Disabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateUser(ctx, b.ID, auth.UserUpdate{Grants: &[]auth.Grant{{RoleID: role.ID, Instance: "*"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Sync(ctx, in); err != nil {
		t.Fatal(err)
	}
	if got := admins(t, in); !slices.Equal(got, []string{bob, manual}) {
		t.Fatalf("running sync: admins %v", got)
	}
	if managed, _ := st.ManagedAdmins(ctx, "srv"); !slices.Equal(managed, []string{bob}) {
		t.Fatalf("managed %v", managed)
	}
	panel, _ := s.PanelAdmins(ctx, "srv")
	if len(panel) != 1 || panel[bob] != "bob" {
		t.Fatalf("panel admins %v", panel)
	}

	// An add with no reply in time may still have been done, so it stays the
	// manager's to take away again.
	c := grant("carol", slow, "srv")
	short, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	if err := s.Sync(short, in); err == nil {
		t.Fatal("no error from an add without a reply")
	}
	if managed, _ := st.ManagedAdmins(ctx, "srv"); !slices.Contains(managed, slow) {
		t.Fatalf("the unanswered add was forgotten: managed %v", managed)
	}
	time.Sleep(1200 * time.Millisecond) // the late reply
	if _, err := svc.UpdateUser(ctx, c.ID, auth.UserUpdate{Disabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	if err := s.Sync(ctx, in); err != nil {
		t.Fatal(err)
	}
	if got := admins(t, in); slices.Contains(got, slow) {
		t.Fatalf("a disabled user is still an admin: %v", got)
	}
}
