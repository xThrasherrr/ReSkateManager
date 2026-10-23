package plugins

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/instance"
	"github.com/xThrasherrr/ReSkateManager/internal/serverconfig"
)

// A plugin's chat commands are the server's own (ReSkate 2.0.3), which it
// reads as it starts: the manager puts the words the plugins that are on ask
// for in each server's config before it starts it (PluginCommands), and
// hands each [command] line to the plugin it's for (command).

// commandGap keeps one player's same command apart, so nobody can make a
// plugin flood chat.
const commandGap = time.Second

// claim is the words an enabled plugin's plugin.toml asks for.
type claim struct {
	plugin  string
	servers []string // none: all of them
	words   []string
}

// claimLocked sets claims from the plugins that are on, in the order a word
// goes to the first that asks: by ID.
func (m *Manager) claimLocked() {
	var out []claim
	for _, p := range m.sortedLocked() {
		p.mu.Lock()
		man, problem := p.manifest, p.problem
		p.mu.Unlock()
		if m.enabled[p.ID] && problem == "" && len(man.Commands) > 0 {
			out = append(out, claim{p.ID, man.Servers, man.Commands})
		}
	}
	m.claims.Store(&out)
}

// PluginCommands is the hook for a server's PluginCommands. A word that's left
// out goes in its plugin's log, saying why. The manager's own words
// (Manager.Words, such as /parks) come first.
func (m *Manager) PluginCommands(def instance.Def) (string, error) {
	var extra []serverconfig.Word
	if m.Words != nil {
		extra = m.Words(def.ID)
	}
	words := slices.Clone(extra)
	if cs := m.claims.Load(); cs != nil {
		for _, c := range *cs {
			if len(c.servers) == 0 || slices.Contains(c.servers, def.ID) {
				for _, w := range c.words {
					words = append(words, serverconfig.Word{Name: w, Plugin: c.plugin})
				}
			}
		}
	}
	left, err := serverconfig.SetPluginCommands(def.ConfigPath(), words)
	if err != nil {
		return "", err
	}
	var msgs []string
	for _, w := range words {
		why, ok := left[w]
		if !ok {
			continue
		}
		text := fmt.Sprintf("/%s isn't on %s: %s.", w.Name, def.ID, why)
		if slices.Contains(extra, w) {
			msgs = append(msgs, "The manager's "+text)
			continue
		}
		msgs = append(msgs, "The plugin "+w.Plugin+"'s "+text)
		if p := m.runningPlugin(w.Plugin); p != nil {
			p.line("warn", text)
		}
		m.write(w.Plugin, "warn", text)
	}
	return strings.Join(msgs, " "), nil
}

func (m *Manager) runningPlugin(id string) *Plugin {
	if runners := m.running.Load(); runners != nil {
		for _, r := range *runners {
			if r.p.ID == id {
				return r.p
			}
		}
	}
	return nil
}

// command hands a player's /word to the plugin it's for, if it runs and has a
// handler for it.
func (m *Manager) command(server string, n instance.Notice, runners []*runner) {
	ev := "command:" + n.Command
	for _, r := range runners {
		if r.p.ID != n.Plugin || !r.onServer(server) || !r.wants(ev) {
			continue
		}
		if m.gaps.ok(server + " " + cmp.Or(n.Player.ID, n.Player.Name) + " " + n.Command) {
			_, payload := event(server, n)
			r.enqueue(job{event: ev, payload: payload})
		}
		return
	}
}

// gaps is when each player last used each command.
type gaps struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func (g *gaps) ok(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	if t, ok := g.last[key]; ok && now.Sub(t) < commandGap {
		return false
	}
	if len(g.last) >= 1024 { // past ones are long over
		g.last = nil
	}
	if g.last == nil {
		g.last = map[string]time.Time{}
	}
	g.last[key] = now
	return true
}
