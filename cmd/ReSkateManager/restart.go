package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/xThrasherrr/ReSkateManager/internal/instance"
)

// restarter remembers which servers were running when an update asked the
// manager to restart, so the new manager starts them again.
type restarter struct {
	mu      sync.Mutex
	pending bool
	resume  []string
}

func (r *restarter) request(reg *instance.Registry, stop func()) {
	r.mu.Lock()
	r.pending = true
	r.resume = nil
	for _, in := range reg.List() {
		if s := in.State(); s == instance.Running || s == instance.Starting {
			r.resume = append(r.resume, in.ID())
		}
	}
	r.mu.Unlock()
	stop()
}

func (r *restarter) requested() (bool, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pending, r.resume
}

func resumePath(dataDir string) string { return filepath.Join(dataDir, "resume.json") }

func writeResume(dataDir string, ids []string) error {
	data, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	return os.WriteFile(resumePath(dataDir), data, 0o644)
}

// takeResume reads and removes the list a restart left behind.
func takeResume(dataDir string) []string {
	data, err := os.ReadFile(resumePath(dataDir))
	if err != nil {
		return nil
	}
	os.Remove(resumePath(dataDir))
	var ids []string
	_ = json.Unmarshal(data, &ids)
	return ids
}

// errRestart ends run so main can hand over to the new binary.
var errRestart = errors.New("restarting for an update")

// relaunchArgs are the new manager's arguments: the same ones, without opening
// another browser tab (the panel that asked reconnects by itself).
func relaunchArgs(args []string) []string {
	if slices.Contains(args, "--no-browser") || slices.Contains(args, "-no-browser") {
		return args
	}
	return append(slices.Clone(args), "--no-browser")
}

// underSystemd: the unit restarts the manager (Restart=on-failure), and a
// child started here would be killed with the unit's old process. A shell
// inherits INVOCATION_ID from the service that runs its terminal, so the test
// is the one systemd documents: stderr is the journal stream it names.
func underSystemd() bool { return os.Getenv("INVOCATION_ID") != "" && stderrIsJournal() }
