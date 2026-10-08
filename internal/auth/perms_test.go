package auth

import "testing"

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
