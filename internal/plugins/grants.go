package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/xThrasherrr/ReSkateManager/internal/auth"
	"github.com/xThrasherrr/ReSkateManager/internal/store"
)

// grantsKey holds what the owner granted each plugin, as a JSON object of
// plugin IDs to permission keys.
const grantsKey = "plugins.granted"

// Grant is a permission a plugin can be given, and the calls it opens.
type Grant struct {
	Key   string   `json:"key"`
	Label string   `json:"label"`
	Calls []string `json:"calls"`
}

// DiscordPost is the one grant that isn't a panel permission: it lets a
// plugin post to Discord webhooks it names, its only way to the network.
const DiscordPost = "discord.post"

// Grants are the panel's own permissions that plugins can be given, on the
// servers they run on, and DiscordPost. A plugin's plugin.toml asks for them,
// and the owner grants them on its card; nothing is granted until then.
var Grants = func() []Grant {
	g := []Grant{
		{Key: auth.PlayersChat, Calls: []string{"server.say", "server.msg", "ui.card"}},
		{Key: auth.PlayersKick, Calls: []string{"server.kick"}},
		{Key: auth.PlayersBan, Calls: []string{"server.ban", "server.unban"}},
		{Key: auth.IngameAdmins, Calls: []string{"server.role", "server.makerole"}},
		{Key: auth.ConsoleExec, Calls: []string{"server.teleport", "server.command"}},
		// server.command's lines need what the panel's console asks of them.
		{Key: auth.SettingsEdit, Calls: []string{"server.command"}},
		{Key: DiscordPost, Label: "Post to Discord webhooks", Calls: []string{"discord.post"}},
	}
	for i := range g {
		if g[i].Label == "" {
			g[i].Label = auth.PermLabel(g[i].Key)
		}
	}
	return g
}()

func grantable(key string) bool {
	return slices.ContainsFunc(Grants, func(g Grant) bool { return g.Key == key })
}

func grantLabel(key string) string {
	if i := slices.IndexFunc(Grants, func(g Grant) bool { return g.Key == key }); i >= 0 {
		return Grants[i].Label
	}
	return auth.PermLabel(key)
}

// ErrBadGrant is a grant the plugin can't be given.
var ErrBadGrant = errors.New("can't grant that")

// loadGrants reads what the owner granted; under m.mu, as Start runs.
func (m *Manager) loadGrants(ctx context.Context) error {
	all := map[string][]string{}
	v, err := m.Store.Get(ctx, grantsKey)
	switch {
	case err == nil:
		if err := json.Unmarshal([]byte(v), &all); err != nil {
			return fmt.Errorf("%s: %w", grantsKey, err)
		}
	case !errors.Is(err, store.ErrNotFound):
		return err
	}
	m.grants.Store(&all)
	return nil
}

// SetGranted replaces what a plugin is granted: permissions from Grants
// that its plugin.toml asks for. It applies at once, to a running plugin too.
func (m *Manager) SetGranted(ctx context.Context, id string, keys []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.scanLocked()
	p, ok := m.plugins[id]
	if !ok {
		return ErrNotFound
	}
	p.mu.Lock()
	asked := p.manifest.Permissions
	p.mu.Unlock()
	keys = slices.Compact(slices.Sorted(slices.Values(keys)))
	for _, k := range keys {
		if !grantable(k) {
			return fmt.Errorf("%w: plugins can't be given %s", ErrBadGrant, k)
		}
		if !slices.Contains(asked, k) {
			return fmt.Errorf("%w: its plugin.toml doesn't ask for %s", ErrBadGrant, k)
		}
	}
	next := map[string][]string{}
	if cur := m.grants.Load(); cur != nil {
		for k, v := range *cur {
			next[k] = v
		}
	}
	if len(keys) == 0 {
		delete(next, id)
	} else {
		next[id] = keys
	}
	b, _ := json.Marshal(next)
	if err := m.Store.Set(ctx, grantsKey, string(b)); err != nil {
		return err
	}
	m.grants.Store(&next)
	return nil
}

// granted is what a plugin may use of what it's granted: only what its
// plugin.toml (as asked) still asks for.
func (m *Manager) granted(id string, asked []string) []string {
	cur := m.grants.Load()
	if cur == nil {
		return nil
	}
	var out []string
	for _, k := range (*cur)[id] {
		if grantable(k) && slices.Contains(asked, k) {
			out = append(out, k)
		}
	}
	return out
}

// perms is a plugin's grants as a user's permissions, on every server: the
// runner checks each call with Can, as the panel checks a user's.
func (m *Manager) perms(id string, asked []string) auth.Perms {
	p := auth.Perms{Global: map[string]bool{}}
	for _, k := range m.granted(id, asked) {
		p.Global[k] = true
	}
	return p
}
