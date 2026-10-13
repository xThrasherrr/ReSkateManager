package serverconfig

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// RolesFile is where a server from ReSkate 2.0.6 on keeps its roles, what
// each may do in the game and who was given which, relative to its folder.
// Its admins are the players given the admin role there: a config's list of
// admins, which older servers keep, is moved into that role when the server
// starts, and the server writes the file then if there is none.
const RolesFile = "data/roles.json"

// AdminRole is the role the server moves a config's admins into, and the one
// the manager gives its in-game admins. A new server's has every command.
const AdminRole = "admin"

// readRoles reads data/roles.json beside a config as a generic tree, so what
// the manager doesn't change survives a write; nil when there is none.
func readRoles(configPath string) (map[string]any, error) {
	data, err := readCapped(filepath.Join(filepath.Dir(configPath), RolesFile), maxConfig)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// The server won't start with roles it can't read either.
	var root map[string]any
	if err := decodeJSON(data).Decode(&root); err != nil {
		return nil, fmt.Errorf("%s is not a JSON object: %w", RolesFile, err)
	}
	if root == nil {
		return nil, fmt.Errorf("%s holds null, not roles", RolesFile)
	}
	return root, nil
}

// ErrNoRoles is a server without data/roles.json: one from before ReSkate
// 2.0.6, or one that hasn't started since it was updated.
var ErrNoRoles = errors.New("this server has no roles yet: ReSkate 2.0.6 and later write data/roles.json the first time they start")

// SetRole gives a player one of the server's roles, or with "" takes theirs
// away, which leaves them the default role. The config's own list of admins
// loses them too: the server would give them the admin role again.
func (f *File) SetRole(id, role string) error {
	if f.roles == nil {
		return ErrNoRoles
	}
	if role != "" && !f.hasRole(role) {
		return fmt.Errorf("the server has no %q role", role)
	}
	players, _ := f.roles["players"].(map[string]any)
	if players == nil {
		players = map[string]any{}
		f.roles["players"] = players
	}
	if role == "" {
		delete(players, id)
	} else {
		players[id] = role
	}
	if v, ok := f.Get("admins"); ok {
		list, _ := v.([]any)
		f.Set("admins", slices.DeleteFunc(slices.Clone(list), func(x any) bool { return fmt.Sprint(x) == id }))
	}
	f.rolesSet = true
	return nil
}

// DefaultRole is the role of every player on a server who was given none, as
// its data/roles.json says; "" without the file, or a default role.
func DefaultRole(configPath string) string {
	roles, _ := readRoles(configPath)
	name, _ := roles["default_role"].(string)
	return name
}

// HasRoles reports whether the server keeps roles in data/roles.json, as
// servers from ReSkate 2.0.6 on do. Their consoles have role and roles
// instead of admin and admins.
func (f *File) HasRoles() bool { return f.roles != nil }

// Role is one of a server's roles, as the panel shows it.
type Role struct {
	Name        string   `json:"name"`
	Tag         string   `json:"tag,omitempty"`   // shown before its players' names in the game
	Color       string   `json:"color,omitempty"` // the tag's, "#RRGGBB"
	Rank        int      `json:"rank"`
	Commands    []string `json:"commands"` // "*" is every command but role and reserved
	Permissions []string `json:"permissions"`
	Inherit     bool     `json:"inherit"` // it also has what the default role has
}

// Roles lists the server's roles in the file's order, who was given which (by
// SteamID64), and the role of everyone given none; no roles on a server
// without them. A role a player was given that the file doesn't define comes
// last, by name alone: the server won't start with one, and says so.
func (f *File) Roles() (list []Role, given map[string]string, defaultRole string) {
	given = f.rolePlayers()
	defaultRole, _ = f.roles["default_role"].(string)
	items, _ := f.roles["roles"].([]any)
	strs := func(v any) []string {
		out := []string{}
		list, _ := v.([]any)
		for _, x := range list {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		r := Role{Commands: strs(m["commands"]), Permissions: strs(m["permissions"]), Inherit: true}
		r.Name, _ = m["name"].(string)
		r.Tag, _ = m["tag"].(string)
		r.Color, _ = m["color"].(string)
		if n, ok := asNumber(m["rank"]); ok {
			r.Rank = int(n)
		}
		if b, ok := m["inherit"].(bool); ok {
			r.Inherit = b
		}
		list = append(list, r)
	}
	for _, name := range slices.Sorted(maps.Values(given)) {
		if !slices.ContainsFunc(list, func(r Role) bool { return r.Name == name }) {
			list = append(list, Role{Name: name, Commands: []string{}, Permissions: []string{}})
		}
	}
	return list, given, defaultRole
}

// rolePlayers is who was given which role, by SteamID64.
func (f *File) rolePlayers() map[string]string {
	out := map[string]string{}
	players, _ := f.roles["players"].(map[string]any)
	for id, role := range players {
		if name, ok := role.(string); ok {
			out[id] = name
		}
	}
	return out
}

// hasRole reports whether the roles have one by this name.
func (f *File) hasRole(name string) bool { return f.role(name) != nil }

// role is the role by this name, as the file has it; nil when there's none.
func (f *File) role(name string) map[string]any {
	list, _ := f.roles["roles"].([]any)
	for _, v := range list {
		if role, _ := v.(map[string]any); role["name"] == name {
			return role
		}
	}
	return nil
}

// Limits of 2.0.6's roles (Server/roles.h, Net/protocol.h).
const (
	maxRoles    = 32
	maxRoleTag  = 16 // bytes
	maxRoleRank = 1000
)

var roleNameRe = regexp.MustCompile(`^[a-z0-9_-]{1,24}$`)

// GameTags are the tags the game uses itself, for the server, a lobby's host,
// friends and the ReSkate team, which no role may wear (roles_error): lower
// case and without spaces, as it compares them. ReSkate's main branch after
// 2.0.7 refuses its admins' tag too, ReSkate Admin.
var GameTags = []string{"server", "reskate", "reskateadmin", "dev", "developer", "contentcreator", "centrix", "staff", "homie", "host", "friend"}

// RoleLook is what the panel sets of a role: its name when it's new, and the
// tag its players wear before their names, the tag's colour, and its rank.
type RoleLook struct {
	Name  string `json:"name"`
	Tag   string `json:"tag"`
	Color string `json:"color"`
	Rank  int    `json:"rank"`
}

// tidy checks the tag, colour and rank as roles_error does, and spells them
// as the server writes them.
func (l *RoleLook) tidy() error {
	l.Tag = strings.TrimSpace(l.Tag)
	if len(l.Tag) > maxRoleTag || !utf8.ValidString(l.Tag) || strings.ContainsFunc(l.Tag, isControl) {
		return fmt.Errorf("a tag is at most %d bytes of plain text", maxRoleTag)
	}
	plain := strings.Map(func(r rune) rune {
		switch {
		case r == ' ':
			return -1
		case 'A' <= r && r <= 'Z':
			return r + 'a' - 'A'
		}
		return r
	}, l.Tag)
	if slices.Contains(GameTags, plain) {
		return fmt.Errorf("the game uses the tag %q itself", l.Tag)
	}
	c := cmp.Or(strings.TrimPrefix(strings.TrimSpace(l.Color), "#"), "FFFFFF")
	if !colorRe.MatchString(c) {
		return errors.New("a tag's colour is like #FF74C6")
	}
	l.Color = "#" + strings.ToUpper(c)
	if l.Rank < 0 || l.Rank > maxRoleRank {
		return fmt.Errorf("a rank is 0 to %d", maxRoleRank)
	}
	return nil
}

// AddRole adds a role with look l. It has what the default role has and
// nothing of its own: the commands and permissions a role gives are set in
// data/roles.json (README-roles.txt beside the server lists them).
func (f *File) AddRole(l RoleLook) error {
	if f.roles == nil {
		return ErrNoRoles
	}
	if !roleNameRe.MatchString(l.Name) || l.Name == "none" {
		return errors.New("a role's name is 1 to 24 of a-z, 0-9, - and _, and not none")
	}
	if f.hasRole(l.Name) {
		return fmt.Errorf("the server has a %q role already", l.Name)
	}
	list, _ := f.roles["roles"].([]any)
	if len(list) >= maxRoles {
		return fmt.Errorf("a server has at most %d roles", maxRoles)
	}
	if err := l.tidy(); err != nil {
		return err
	}
	f.roles["roles"] = append(list, map[string]any{"name": l.Name, "tag": l.Tag, "color": l.Color,
		"rank": json.Number(strconv.Itoa(l.Rank)), "inherit": true, "commands": []any{}, "permissions": []any{}})
	f.rolesSet = true
	return nil
}

// EditRole sets a role's tag, colour and rank to l's; whatever else it has
// stays as it is.
func (f *File) EditRole(name string, l RoleLook) error {
	if f.roles == nil {
		return ErrNoRoles
	}
	role := f.role(name)
	if role == nil {
		return fmt.Errorf("the server has no %q role", name)
	}
	if err := l.tidy(); err != nil {
		return err
	}
	role["tag"], role["color"], role["rank"] = l.Tag, l.Color, json.Number(strconv.Itoa(l.Rank))
	f.rolesSet = true
	return nil
}

// DeleteRole removes a role, and its players are the default role again, as
// in the server's own roles editor; it says how many there were. The default
// role stays, and so does admin: the manager gives it to its in-game admins.
func (f *File) DeleteRole(name string) (int, error) {
	if f.roles == nil {
		return 0, ErrNoRoles
	}
	if !f.hasRole(name) {
		return 0, fmt.Errorf("the server has no %q role", name)
	}
	if def, _ := f.roles["default_role"].(string); name == def {
		return 0, errors.New("the default role stays: it's everyone's who was given none")
	}
	if name == AdminRole {
		return 0, errors.New("the admin role stays: the manager gives it to its in-game admins")
	}
	list, _ := f.roles["roles"].([]any)
	f.roles["roles"] = slices.DeleteFunc(slices.Clone(list), func(v any) bool {
		role, _ := v.(map[string]any)
		return role["name"] == name
	})
	players, _ := f.roles["players"].(map[string]any)
	n := 0
	for id, role := range players {
		if role == name {
			delete(players, id)
			n++
		}
	}
	f.rolesSet = true
	return n, nil
}

// setAdminRole gives the admin role to ids and takes it from everyone else
// who has it, who are then the default role again. The config's own list
// goes: the server would give its players the role again.
func (f *File) setAdminRole(ids []string) error {
	given := f.rolePlayers()
	for _, id := range ids {
		if given[id] != AdminRole && !f.hasRole(AdminRole) {
			return fmt.Errorf("%s has no %q role to give", RolesFile, AdminRole)
		}
	}
	players := map[string]any{}
	for id, role := range given {
		if role != AdminRole || slices.Contains(ids, id) {
			players[id] = role
		}
	}
	for _, id := range ids {
		players[id] = AdminRole
	}
	f.roles["players"] = players
	f.del("admins")
	f.rolesSet = true
	return nil
}

// KeepRoles puts back the roles a server had before a restore, old being its
// config then; an import, with no old, keeps none. The roles say who may run
// which of the server's commands in the game, so a backup's are only for
// someone who could set them. It needs nothing of the restored config, which
// may be one the server can't read until someone fixes it.
func KeepRoles(configPath string, old *File) error {
	f := &File{}
	if old != nil {
		f.roles = old.roles
	}
	return f.WriteRoles(configPath)
}

// WriteRoles writes data/roles.json as the roles now are, or removes it when
// there are none to keep.
func (f *File) WriteRoles(configPath string) error {
	path := filepath.Join(filepath.Dir(configPath), RolesFile)
	if f.roles == nil {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	return writeJSON(path, f.roles)
}
