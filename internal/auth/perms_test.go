package auth

import (
	"context"
	"reflect"
	"testing"
)

func TestCovers(t *testing.T) {
	perms := func(global []string, byInstance map[string][]string) Perms {
		p := Perms{Global: map[string]bool{}, ByInstance: map[string]map[string]bool{}}
		p.add(global, "*")
		for inst, keys := range byInstance {
			p.add(keys, inst)
		}
		return p
	}
	mod := perms([]string{PanelUsersManage, PlayersKick}, map[string][]string{"a": {PlayersBan}})

	for _, c := range []struct {
		what string
		q    Perms
		want bool
	}{
		{"nothing", perms(nil, nil), true},
		{"itself", mod, true},
		{"a global perm on one server", perms(nil, map[string][]string{"b": {PlayersKick}}), true},
		{"a server perm on that server", perms(nil, map[string][]string{"a": {PlayersBan}}), true},
		{"a server perm on another server", perms(nil, map[string][]string{"b": {PlayersBan}}), false},
		{"a server perm on all servers", perms([]string{PlayersBan}, nil), false},
		{"a perm it lacks", perms([]string{AuditView}, nil), false},
		{"an owner", Perms{Owner: true}, false},
	} {
		if got := mod.Covers(c.q); got != c.want {
			t.Errorf("covers %s: %v, want %v", c.what, got, c.want)
		}
	}
	if !(Perms{Owner: true}).Covers(Perms{Owner: true}) {
		t.Error("an owner should cover an owner")
	}
	if !mod.HoldsAll([]string{PlayersKick}) || mod.HoldsAll([]string{PlayersBan}) {
		t.Error("HoldsAll should only count perms held on all servers")
	}

	// A key this manager doesn't know grants nothing, not even a look.
	odd := perms([]string{"servers.everything"}, nil)
	if odd.Sees("a") || len(odd.Global) != 0 {
		t.Errorf("an unknown key counted: %+v", odd)
	}
	if !perms([]string{InstancesManage}, nil).Sees("a") {
		t.Error("managing servers should see every one")
	}
}

// SteamUsers resolves what each user may do just as Perms does.
func TestSteamUsers(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	admin, err := s.SaveRole(ctx, Role{Name: "In-game", Permissions: []string{IngameAdmin, AuditView}})
	if err != nil {
		t.Fatal(err)
	}
	mod, err := s.SaveRole(ctx, Role{Name: "Kicks", Permissions: []string{PlayersKick}})
	if err != nil {
		t.Fatal(err)
	}
	add := func(name, steam string, owner bool, grants ...Grant) *User {
		u, err := s.CreateUser(ctx, NewUser{Username: name, Password: "correct horse battery staple", SteamID: steam, Owner: owner, Grants: grants})
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	add("owner", "76561198000000001", true, Grant{RoleID: mod.ID, Instance: "*"})
	add("alice", "76561198000000002", false, Grant{RoleID: admin.ID, Instance: "a"}, Grant{RoleID: mod.ID, Instance: "*"})
	add("bob", "76561198000000003", false, Grant{RoleID: admin.ID, Instance: "*"})
	add("erin", "76561198000000005", false)
	add("dave", "", false, Grant{RoleID: admin.ID, Instance: "*"})
	carol := add("carol", "76561198000000004", false, Grant{RoleID: admin.ID, Instance: "*"})
	disabled := true
	if _, err := s.UpdateUser(ctx, carol.ID, UserUpdate{Disabled: &disabled}); err != nil {
		t.Fatal(err)
	}

	got, err := s.SteamUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	users, err := s.Users(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var want []*SteamUser
	for _, u := range users {
		if u.Disabled || u.SteamID == "" {
			continue
		}
		p, err := s.Perms(ctx, u)
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, &SteamUser{SteamID: u.SteamID, Username: u.Username, Perms: p})
	}
	if len(want) != 4 || !reflect.DeepEqual(got, want) {
		t.Errorf("steam users:\n got %+v\nwant %+v", got, want)
	}
}
