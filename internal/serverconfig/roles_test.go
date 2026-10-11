package serverconfig

import (
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// A server from ReSkate 2.0.6: its admins are in data/roles.json.
func rolesServer(t *testing.T, config, roles string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "ReSkateServer.json")
	if err := os.WriteFile(path, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	if roles != "" {
		if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, RolesFile), []byte(roles), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

const newRoles = `{
  "default_role": "player",
  "roles": [
    {"name": "player", "tag": "", "color": "#FFFFFF", "rank": 0, "inherit": true, "commands": [], "permissions": ["chat"]},
    {"name": "moderator", "tag": "Mod", "color": "#5EC8FF", "rank": 50, "inherit": true, "commands": ["kick"], "permissions": []},
    {"name": "admin", "tag": "Admin", "color": "#FF74C6", "rank": 100, "inherit": true, "commands": ["*", "role", "reserved"], "permissions": []}
  ],
  "players": {"76561198000000001": "admin", "76561198000000002": "moderator"}
}`

func readRolesFile(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(filepath.Dir(path), RolesFile))
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestAdminsInRoles(t *testing.T) {
	// An admin the manager listed while the server was stopped, not yet moved.
	path := rolesServer(t, `{"server": {"name": "x"}, "access": {"admins": ["76561198000000003"]}}`, newRoles)
	f, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if !f.HasRoles() || DefaultRole(path) != "player" {
		t.Fatalf("roles %v, default %q", f.HasRoles(), DefaultRole(path))
	}
	if got := f.Admins(); !slices.Equal(got, []string{"76561198000000003", "76561198000000001"}) {
		t.Errorf("admins %v", got)
	}
	if !slices.Contains(f.Gone(), "admins") {
		t.Errorf("gone %v", f.Gone())
	}

	// One stays, one goes back to the default role, one is new; the
	// moderator and the rest of the file are left as they were.
	if err := f.SetAdmins([]string{"76561198000000003", "76561198000000004"}); err != nil {
		t.Fatal(err)
	}
	if err := f.Write(path); err != nil {
		t.Fatal(err)
	}
	roles := readRolesFile(t, path)
	want := map[string]any{"76561198000000002": "moderator", "76561198000000003": "admin", "76561198000000004": "admin"}
	if players := roles["players"].(map[string]any); !mapsEqual(players, want) {
		t.Errorf("players %v", players)
	}
	if list := roles["roles"].([]any); len(list) != 3 || roles["default_role"] != "player" {
		t.Errorf("roles changed: %v", roles)
	}
	f, err = Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, listed := f.Get("admins"); listed {
		t.Error("the config still lists admins, which the server would give the role again")
	}
	if got := f.Admins(); !slices.Equal(got, []string{"76561198000000003", "76561198000000004"}) {
		t.Errorf("admins read back %v", got)
	}
}

func mapsEqual(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// An owner can delete the admin role; nobody can be given it then.
func TestNoAdminRole(t *testing.T) {
	path := rolesServer(t, `{"server": {"name": "x"}}`,
		`{"default_role": "", "roles": [{"name": "boss", "commands": ["*"]}], "players": {"76561198000000001": "boss"}}`)
	f, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.SetAdmins([]string{"76561198000000002"}); err == nil {
		t.Error("gave a role the server doesn't have")
	}
	if err := f.SetAdmins(nil); err != nil {
		t.Errorf("taking nobody's role: %v", err)
	}
}

func TestBrokenRoles(t *testing.T) {
	path := rolesServer(t, `{"server": {"name": "x"}}`, `{"roles": [`)
	if _, err := Read(path); err == nil {
		t.Error("read a config beside roles the server can't read")
	}
}

// Before 2.0.6 the config lists the admins, and has no roles to keep.
func TestAdminsBeforeRoles(t *testing.T) {
	path := rolesServer(t, `{"server": {"name": "x"}, "access": {"admins": ["76561198000000001"]}}`, "")
	f, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if f.HasRoles() || slices.Contains(f.Gone(), "admins") || DefaultRole(path) != "" {
		t.Errorf("roles %v, gone %v", f.HasRoles(), f.Gone())
	}
	if err := f.SetAdmins([]string{"76561198000000002"}); err != nil {
		t.Fatal(err)
	}
	if err := f.Write(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), RolesFile)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("wrote roles for a server without them: %v", err)
	}
	if f, _ = Read(path); !slices.Equal(f.Admins(), []string{"76561198000000002"}) {
		t.Errorf("admins %v", f.Admins())
	}
}

// A restore puts the roles back as they were; an import keeps none.
func TestKeepRoles(t *testing.T) {
	path := rolesServer(t, `{"server": {"name": "x"}}`, newRoles)
	old, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	everyone := `{"default_role": "player", "roles": [{"name": "player", "commands": ["*"]}], "players": {}}`
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), RolesFile), []byte(everyone), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := KeepRoles(path, old); err != nil {
		t.Fatal(err)
	}
	if f, err := Read(path); err != nil || !slices.Equal(f.Admins(), []string{"76561198000000001"}) || len(f.roles["roles"].([]any)) != 3 {
		t.Errorf("roles after a restore: %v, %v", f, err)
	}
	if err := KeepRoles(path, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), RolesFile)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("an import kept its roles: %v", err)
	}
}

// The Roles card: each role as the file has it, who was given which, and the
// default; a player given a role the file lacks still shows.
func TestRolesList(t *testing.T) {
	path := rolesServer(t, `{"server": {"name": "x"}}`, `{"default_role": "player", "roles": [
		{"name": "player", "permissions": ["chat"]},
		{"name": "muted", "inherit": false, "commands": [], "permissions": ["votes"]},
		{"name": "admin", "tag": "Admin", "color": "#FF74C6", "rank": 100, "commands": ["*", "role"]}],
		"players": {"76561198000000001": "admin", "76561198000000002": "muted", "76561198000000003": "ghost"}}`)
	f, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	list, given, def := f.Roles()
	if def != "player" || len(given) != 3 || given["76561198000000002"] != "muted" {
		t.Errorf("default %q, given %v", def, given)
	}
	var names []string
	for _, r := range list {
		names = append(names, r.Name)
	}
	if !slices.Equal(names, []string{"player", "muted", "admin", "ghost"}) {
		t.Fatalf("roles %v", names)
	}
	if a := list[2]; a.Tag != "Admin" || a.Color != "#FF74C6" || a.Rank != 100 || !slices.Equal(a.Commands, []string{"*", "role"}) || !a.Inherit {
		t.Errorf("admin %+v", a)
	}
	if m := list[1]; m.Inherit || !slices.Equal(m.Permissions, []string{"votes"}) {
		t.Errorf("muted %+v", m)
	}

	path = rolesServer(t, `{"server": {"name": "x"}}`, "")
	if f, _ = Read(path); f.HasRoles() {
		t.Error("roles without the file")
	}
	if list, given, def = f.Roles(); len(list) != 0 || len(given) != 0 || def != "" {
		t.Errorf("roles of a server without them: %v %v %q", list, given, def)
	}
}

// Giving a role on a stopped server: in data/roles.json, and off the config's
// own list of admins, which would make them an admin again.
func TestSetRole(t *testing.T) {
	path := rolesServer(t, `{"server": {"name": "x"}, "access": {"admins": ["76561198000000003"]}}`, newRoles)
	f, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.SetRole("76561198000000003", "moderator"); err != nil {
		t.Fatal(err)
	}
	if err := f.SetRole("76561198000000002", ""); err != nil {
		t.Fatal(err)
	}
	if err := f.SetRole("76561198000000004", "ghost"); err == nil {
		t.Error("gave a role the server doesn't have")
	}
	if err := f.Write(path); err != nil {
		t.Fatal(err)
	}
	f, _ = Read(path)
	_, given, _ := f.Roles()
	if want := map[string]string{"76561198000000001": "admin", "76561198000000003": "moderator"}; !maps.Equal(given, want) {
		t.Errorf("given %v", given)
	}
	if got := f.Admins(); !slices.Equal(got, []string{"76561198000000001"}) {
		t.Errorf("admins %v", got)
	}
	g, _ := Read(rolesServer(t, `{"server": {"name": "x"}}`, ""))
	if err := g.SetRole("76561198000000001", "vip"); !errors.Is(err, ErrNoRoles) {
		t.Errorf("a server without roles: %v", err)
	}
}
