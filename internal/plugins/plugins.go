// Package plugins runs the owner's Lua plugins inside the manager. Each runs
// in a Lua of its own (package luavm) on a goroutine of its own, hears the
// servers' events through a queue of its own, and reaches nothing but the
// plugin API the prelude builds. A plugin that keeps failing or running past
// its time is stopped, and its card says why.
package plugins

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/alerts"
	"github.com/xThrasherrr/ReSkateManager/internal/instance"
	"github.com/xThrasherrr/ReSkateManager/internal/logfile"
	"github.com/xThrasherrr/ReSkateManager/internal/plugins/luavm"
	"github.com/xThrasherrr/ReSkateManager/internal/store"
)

//go:embed prelude.lua
var prelude string

// enabledKey holds which plugins the owner turned on, as a JSON list of IDs.
const enabledKey = "plugins.enabled"

// ErrNotFound is a plugin ID with no plugin.
var ErrNotFound = errors.New("no such plugin")

// Manager runs the plugins in Dir.
type Manager struct {
	Dir     string // a folder per plugin, its name the plugin's ID
	Store   *store.Store
	Log     *slog.Logger // the manager's log
	LogPath string       // where plugins' log lines go, as data/plugins.log
	Servers Servers      // the servers plugins read and act on; nil for none
	// Discord posts a plugin's discord.post to its webhook; nil for none.
	Discord func(ctx context.Context, webhook string, a alerts.Alert) error

	mu      sync.Mutex // guards what follows, and starting and stopping runners
	plugins map[string]*Plugin
	enabled map[string]bool
	rt      *luavm.Runtime
	out     *logfile.Writer
	data    *data
	closed  bool

	runMu   sync.Mutex                          // guards changes to running
	running atomic.Pointer[[]*runner]           // for Notice, which can't wait on a lock
	grants  atomic.Pointer[map[string][]string] // for the runners, as they call
	claims  atomic.Pointer[[]claim]             // for PluginCommands, under a server's lock
	gaps    gaps                                // between a player's commands
}

// Plugin is one folder in Dir, and what became of it.
type Plugin struct {
	ID  string
	dir string

	mu       sync.Mutex // guards what follows; the runner writes it
	manifest Manifest
	unknown  []string // settings in plugin.toml that do nothing
	problem  string   // why it can't start as it is
	run      *runner
	state    string // Off, Starting, Running, Failed or Stopped
	reason   string
	since    time.Time
	restarts int
	stats    Stats
	recent   []Line // the newest last
	failures []Line
}

// Plugin states.
const (
	Off      = "off"
	Starting = "starting"
	Running  = "running"
	Failed   = "failed"  // couldn't start
	Stopped  = "stopped" // stopped by the manager, for running past its time or failing
)

// Stats counts what a plugin did since it started.
type Stats struct {
	Events   uint64   `json:"events"`
	Dropped  uint64   `json:"dropped"` // events and timers it was too busy for
	Calls    uint64   `json:"calls"`
	Errors   uint64   `json:"errors"`
	Slowest  float64  `json:"slowestMs"`
	Memory   int64    `json:"memory"`
	Timers   int      `json:"timers"`
	Handlers []string `json:"handlers"`
	Sent     uint64   `json:"sent"`   // commands sent to its servers
	Unsent   uint64   `json:"unsent"` // commands not sent: too many were waiting
}

// Line is a line in a plugin's log.
type Line struct {
	At    int64  `json:"at"`    // unix ms
	Level string `json:"level"` // info, warn or error
	Text  string `json:"text"`
}

const (
	keepRecent   = 50
	keepFailures = 10
)

// Status is a plugin as the panel shows it.
type Status struct {
	ID  string `json:"id"`
	Dir string `json:"dir"`
	Manifest
	Unknown  []string `json:"unknown,omitempty"`
	Granted  []string `json:"granted"` // of the permissions it asks for
	Problem  string   `json:"problem,omitempty"`
	Enabled  bool     `json:"enabled"`
	State    string   `json:"state"`
	Reason   string   `json:"reason,omitempty"`
	Since    int64    `json:"since,omitempty"`
	Restarts int      `json:"restarts"`
	Stats    Stats    `json:"stats"`
	Recent   []Line   `json:"recent"`
	Failures []Line   `json:"failures"`
}

// Start reads which plugins are on and starts them.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := os.MkdirAll(m.Dir, 0o755); err != nil {
		return err
	}
	out, err := logfile.Open(m.LogPath)
	if err != nil {
		return err
	}
	m.out = out
	m.enabled = map[string]bool{}
	if v, err := m.Store.Get(ctx, enabledKey); err == nil {
		var ids []string
		if err := json.Unmarshal([]byte(v), &ids); err != nil {
			return fmt.Errorf("%s: %w", enabledKey, err)
		}
		for _, id := range ids {
			m.enabled[id] = true
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if err := m.loadGrants(ctx); err != nil {
		return err
	}
	if m.data, err = openData(ctx, m.Store, m.Log); err != nil {
		return err
	}
	m.cleanUploads()
	m.scanLocked()
	for _, p := range m.sortedLocked() {
		if m.enabled[p.ID] {
			m.startLocked(p)
		}
	}
	return nil
}

// Close stops every plugin.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	for _, p := range m.plugins {
		m.stopLocked(p, "")
	}
	if m.data != nil {
		m.data.close()
		m.data = nil
	}
	if m.rt != nil {
		m.rt.Close(context.Background())
		m.rt = nil
	}
	if m.out != nil {
		m.out.Close()
	}
}

// List is every plugin in Dir, read again from it.
func (m *Manager) List() []Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.scanLocked()
	var out []Status
	for _, p := range m.sortedLocked() {
		out = append(out, m.status(p))
	}
	return out
}

// Get is one plugin, read again from Dir.
func (m *Manager) Get(id string) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.scanLocked()
	p, ok := m.plugins[id]
	if !ok {
		return Status{}, ErrNotFound
	}
	return m.status(p), nil
}

func (m *Manager) status(p *Plugin) Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := Status{ID: p.ID, Dir: p.dir, Manifest: p.manifest, Unknown: p.unknown, Problem: p.problem,
		Enabled: m.enabled[p.ID], State: p.state, Reason: p.reason, Restarts: p.restarts, Stats: p.stats,
		Recent: slices.Clone(p.recent), Failures: slices.Clone(p.failures)}
	if !p.since.IsZero() {
		s.Since = p.since.UnixMilli()
	}
	if s.Recent == nil {
		s.Recent = []Line{}
	}
	if s.Failures == nil {
		s.Failures = []Line{}
	}
	if s.Granted = m.granted(p.ID, p.manifest.Permissions); s.Granted == nil {
		s.Granted = []string{}
	}
	return s
}

// SetEnabled turns a plugin on, starting it, or off, stopping it.
func (m *Manager) SetEnabled(ctx context.Context, id string, on bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.scanLocked()
	p, ok := m.plugins[id]
	if !ok {
		return ErrNotFound
	}
	if m.enabled[id] == on {
		return nil
	}
	ids := []string{}
	for k := range m.enabled {
		if k != id {
			ids = append(ids, k)
		}
	}
	if on {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	b, _ := json.Marshal(ids)
	if err := m.Store.Set(ctx, enabledKey, string(b)); err != nil {
		return err
	}
	m.enabled[id] = on
	m.claimLocked()
	if on {
		p.clearStats()
		m.startLocked(p)
	} else {
		m.stopLocked(p, "Turned off.")
	}
	return nil
}

// Reload reads a plugin's files again and starts it afresh, if it's on.
func (m *Manager) Reload(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.scanLocked()
	p, ok := m.plugins[id]
	if !ok {
		return ErrNotFound
	}
	m.stopLocked(p, "Reloaded.")
	p.clearStats()
	m.readLocked(p)
	m.claimLocked()
	if m.enabled[id] {
		m.startLocked(p)
	}
	return nil
}

// scanLocked finds the plugin folders in Dir. One that's gone stays listed
// while it runs.
func (m *Manager) scanLocked() {
	if m.plugins == nil {
		m.plugins = map[string]*Plugin{}
	}
	entries, err := os.ReadDir(m.Dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		m.Log.Warn("plugins folder", "err", err)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		id := e.Name()
		if !idRe.MatchString(id) {
			continue
		}
		dir := filepath.Join(m.Dir, id)
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			continue
		}
		seen[id] = true
		p, ok := m.plugins[id]
		if !ok {
			p = &Plugin{ID: id, dir: dir, state: Off}
			m.plugins[id] = p
		}
		if p.run == nil || p.run.ended() {
			m.readLocked(p)
		}
	}
	for id, p := range m.plugins {
		if seen[id] {
			continue
		}
		if p.run != nil && !p.run.ended() {
			p.mu.Lock()
			p.problem = "Its folder is gone; it runs until turned off."
			p.mu.Unlock()
			continue
		}
		delete(m.plugins, id)
	}
	m.claimLocked()
}

// readLocked reads a plugin's plugin.toml again, and checks it has a main.lua.
func (m *Manager) readLocked(p *Plugin) {
	man, unknown, err := readManifest(p.dir)
	problem := ""
	if err != nil {
		problem = err.Error()
	} else if _, err := os.Stat(filepath.Join(p.dir, mainFile)); err != nil {
		problem = "It has no " + mainFile + "."
	}
	p.mu.Lock()
	p.manifest, p.unknown, p.problem = man, unknown, problem
	p.mu.Unlock()
}

func (m *Manager) sortedLocked() []*Plugin {
	out := make([]*Plugin, 0, len(m.plugins))
	for _, p := range m.plugins {
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b *Plugin) int { return strings.Compare(strings.ToLower(a.ID), strings.ToLower(b.ID)) })
	return out
}

func (m *Manager) runtimeLocked() (*luavm.Runtime, error) {
	if m.rt == nil {
		rt, err := luavm.NewRuntime(context.Background())
		if err != nil {
			return nil, err
		}
		m.rt = rt
	}
	return m.rt, nil
}

func (m *Manager) startLocked(p *Plugin) {
	if m.closed || (p.run != nil && !p.run.ended()) {
		return
	}
	p.mu.Lock()
	problem, man := p.problem, p.manifest
	p.mu.Unlock()
	if problem != "" {
		p.setState(Failed, problem)
		return
	}
	rt, err := m.runtimeLocked()
	if err != nil {
		p.setState(Failed, "Lua didn't start: "+err.Error())
		m.Log.Error("plugins: Lua didn't start", "err", err)
		return
	}
	r := newRunner(m, p, man, rt)
	p.mu.Lock()
	p.run = r
	p.mu.Unlock()
	p.setState(Starting, "") // now, so whoever turned it on sees it start
	m.list(r)
	go r.loop()
}

// stopLocked stops a plugin if it runs, and waits until it has. A reason
// marks it off in the log; "" stops it quietly.
func (m *Manager) stopLocked(p *Plugin, reason string) {
	p.mu.Lock()
	r := p.run
	p.run = nil
	p.mu.Unlock()
	if r != nil {
		r.stop()
	}
	if reason != "" && r != nil {
		p.line("info", reason)
		m.write(p.ID, "info", reason)
	}
	p.setState(Off, "")
}

func (m *Manager) list(r *runner) {
	m.runMu.Lock()
	defer m.runMu.Unlock()
	var next []*runner
	if cur := m.running.Load(); cur != nil {
		next = slices.Clone(*cur)
	}
	next = append(next, r)
	m.running.Store(&next)
}

func (m *Manager) unlist(r *runner) {
	m.runMu.Lock()
	defer m.runMu.Unlock()
	cur := m.running.Load()
	if cur == nil {
		return
	}
	next := slices.DeleteFunc(slices.Clone(*cur), func(x *runner) bool { return x == r })
	m.running.Store(&next)
}

// Notice is the hook for a server's OnNotice: it queues the event for each
// plugin that listens to it on that server, never waiting.
func (m *Manager) Notice(server string) func(instance.Notice) {
	return func(n instance.Notice) {
		runners := m.running.Load()
		if runners == nil || len(*runners) == 0 {
			return
		}
		if n.Kind == instance.NoticeCommand {
			m.command(server, n, *runners)
			return
		}
		var name string
		var payload map[string]any
		for _, r := range *runners {
			if !r.onServer(server) {
				continue
			}
			if payload == nil {
				if name, payload = event(server, n); name == "" {
					return
				}
			}
			if r.wants(name) {
				r.enqueue(job{event: name, payload: payload})
			}
		}
	}
}

// clearStats starts its counts afresh, as it starts again; its log stays.
func (p *Plugin) clearStats() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.restarts, p.stats, p.failures = 0, Stats{}, nil
}

func (p *Plugin) setState(state, reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state, p.reason = state, reason
	switch state {
	case Starting:
		p.since = time.Time{}
	case Running:
		p.since = time.Now()
	case Off:
		p.since = time.Time{}
	}
}

// line keeps a line for the plugin's card; logs go to the file too (runner.log).
func (p *Plugin) line(level, text string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	l := Line{At: time.Now().UnixMilli(), Level: level, Text: text}
	if len(p.recent) >= keepRecent {
		p.recent = slices.Delete(p.recent, 0, 1)
	}
	p.recent = append(p.recent, l)
	if level == "error" {
		if len(p.failures) >= keepFailures {
			p.failures = slices.Delete(p.failures, 0, 1)
		}
		p.failures = append(p.failures, l)
	}
}

// write puts a plugin's line in plugins.log.
func (m *Manager) write(id, level, text string) {
	if m.out == nil {
		return
	}
	text = strings.ReplaceAll(strings.TrimRight(text, "\n"), "\n", "\n\t")
	fmt.Fprintf(m.out, "%s %s %s: %s\n", time.Now().Format("2006-01-02 15:04:05"), id, level, text)
}
