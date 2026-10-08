package api

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/alerts"
	"github.com/xThrasherrr/ReSkateManager/internal/host"
	"github.com/xThrasherrr/ReSkateManager/internal/instance"
	"github.com/xThrasherrr/ReSkateManager/internal/serverconfig"
)

// ManagerLog is the manager's own log, in data/.
const ManagerLog = "manager.log"

const (
	// diskStale is how old a measurement of what each part of the manager's
	// folder holds gets before the Host page asking for it measures again.
	// Walking big Mods folders isn't free, so it isn't done on every look,
	// and not at all while nobody looks.
	diskStale = 10 * time.Minute
	// diskAgain is how soon after a measurement someone can ask for another.
	diskAgain = 30 * time.Second
)

// diskMeasure is what each part of the manager's folder held when measured.
type diskMeasure struct {
	at                                        time.Time
	data, managerLogs, shared, backups, cache int64
	servers                                   map[string]serverDisk // by instance id
}

// serverDisk splits a server's folder into its own mods, its logs and the
// rest: the program, its config and world layers.
type serverDisk struct{ files, mods, logs int64 }

// diskPart is what one kind of thing the manager keeps holds on one drive:
// servers (their program files and own mods), logs (theirs and the
// manager's), shared (mods), backups, cache (server releases) or data (the
// database and settings).
type diskPart struct {
	Kind   string `json:"kind"`
	Volume string `json:"volume"`
	Size   int64  `json:"size"`
}

type diskServer struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Volume string `json:"volume"`
	Files  int64  `json:"files"`
	Mods   int64  `json:"mods"` // its own; shared ones count under shared
	Logs   int64  `json:"logs"`
}

// hostDisk answers with each drive the manager keeps things on, what it keeps
// there, and the free space a drive counts as low under. Free space is read on
// each call; the parts come from the last measurement, and a new one starts
// when it is stale, on ?fresh, or when a server was added since. The answer
// says when one is running, so the page can ask again soon.
func (a *API) hostDisk(w http.ResponseWriter, r *http.Request) {
	a.diskMu.Lock()
	m := a.disk
	older := func(d time.Duration) bool { return m == nil || time.Since(m.at) > d }
	added := m != nil && slices.ContainsFunc(a.Reg.List(), func(in *instance.Instance) bool {
		_, ok := m.servers[in.ID()]
		return !ok
	})
	if !a.diskBusy && (older(diskStale) || (added || r.URL.Query().Has("fresh")) && older(diskAgain)) {
		a.diskBusy = true
		go a.measureDisk()
	}
	busy := a.diskBusy
	a.diskMu.Unlock()

	drives, vols := host.Drives(a.DiskDirs())
	set, err := alerts.Load(r.Context(), a.Store)
	if err != nil {
		a.internalErr(w, r, err)
		return
	}

	parts := []diskPart{}
	servers := []diskServer{}
	var at int64
	if m != nil {
		at = m.at.Unix()
		add := func(kind, vol string, n int64) {
			for i := range parts {
				if parts[i].Kind == kind && parts[i].Volume == vol {
					parts[i].Size += n
					return
				}
			}
			parts = append(parts, diskPart{Kind: kind, Volume: vol, Size: n})
		}
		for _, in := range a.Reg.List() {
			s, ok := m.servers[in.ID()]
			if !ok {
				continue // added since
			}
			vol := vols[in.Def().Dir]
			servers = append(servers, diskServer{ID: in.ID(), Name: in.Def().Name, Volume: vol, Files: s.files, Mods: s.mods, Logs: s.logs})
			add("servers", vol, s.files+s.mods)
			add("logs", vol, s.logs)
		}
		if a.DataDir != "" {
			add("logs", vols[a.DataDir], m.managerLogs)
			add("data", vols[a.DataDir], m.data)
		}
		for _, p := range []struct {
			kind, dir string
			size      int64
		}{{"shared", a.SharedDir, m.shared}, {"backups", a.backupsDir(), m.backups}, {"cache", a.CacheDir, m.cache}} {
			if p.dir != "" {
				add(p.kind, vols[p.dir], p.size)
			}
		}
	}
	writeJSON(w, 200, map[string]any{"at": at, "measuring": busy, "drives": drives, "parts": parts, "servers": servers,
		"lowFree": int64(set.DiskGB) << 30})
}

// DiskDirs are the folders the manager keeps things in, its own first, then
// each server's: what the Host page and the disk alert look at.
func (a *API) DiskDirs() []string {
	dirs := []string{a.DataDir, a.ServersDir, a.SharedDir, a.backupsDir(), a.CacheDir}
	for _, in := range a.Reg.List() {
		dirs = append(dirs, in.Def().Dir)
	}
	return dirs
}

func (a *API) backupsDir() string {
	if a.Backups == nil {
		return ""
	}
	return a.Backups.Dir
}

func (a *API) measureDisk() {
	m := &diskMeasure{servers: map[string]serverDisk{}}
	defer func() {
		a.diskMu.Lock()
		a.disk, a.diskBusy = m, false
		a.diskMu.Unlock()
	}()
	for _, in := range a.Reg.List() {
		m.servers[in.ID()] = measureServer(in.Def().Dir)
	}
	if a.DataDir != "" {
		for _, f := range instance.LogFiles(a.DataDir, ManagerLog) {
			m.managerLogs += f.Size
		}
		m.data = max(dirSize(a.DataDir)-m.managerLogs, 0)
	}
	m.shared, m.backups, m.cache = dirSize(a.SharedDir), dirSize(a.backupsDir()), dirSize(a.CacheDir)
	m.at = time.Now()
}

// measureServer splits server folder dir into its own mods, its logs and the
// rest. Links to shared mods aren't followed.
func measureServer(dir string) serverDisk {
	var s serverDisk
	logs := map[string]bool{}
	for _, name := range []string{instance.ServerLog, instance.ConsoleLog} {
		for _, f := range instance.LogFiles(dir, name) {
			s.logs += f.Size
			logs[f.Name] = true
		}
	}
	mods, disabled := filepath.Base(serverconfig.ModsDir(dir)), filepath.Base(serverconfig.DisabledModsDir(dir))
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		switch {
		case e.IsDir() && (strings.EqualFold(e.Name(), mods) || strings.EqualFold(e.Name(), disabled)):
			s.mods += dirSize(filepath.Join(dir, e.Name()))
		case e.IsDir():
			s.files += dirSize(filepath.Join(dir, e.Name()))
		case e.Type().IsRegular() && !logs[e.Name()]:
			if fi, err := e.Info(); err == nil {
				s.files += fi.Size()
			}
		}
	}
	return s
}

// dirSize is host.DirSize with nothing for no folder, or one it can't read.
func dirSize(dir string) int64 {
	if dir == "" {
		return 0
	}
	n, _ := host.DirSize(dir)
	return n
}
