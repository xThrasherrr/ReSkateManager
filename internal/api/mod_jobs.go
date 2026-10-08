package api

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/instance"
	"github.com/xThrasherrr/ReSkateManager/internal/serverconfig"
	"github.com/xThrasherrr/ReSkateManager/internal/thunderstore"
)

// A mod from Thunderstore is downloaded and installed in the background, as a
// job the panel polls: a big map takes longer than a proxy lets one request
// run (Cloudflare gives up after 100 seconds).
const (
	// maxModDownload caps a download from Thunderstore. It needs no chunks, so
	// only the size the mod may unpack to limits it.
	maxModDownload = 8 << 30
)

type modJob struct {
	jobBase
	target string // the instance it installs to, or sharedTarget
	owner  bool   // the user is an owner, who may see what the system said
	pkg    string // lowercased, so one package is not installed twice at once
	st     modJobStatus
}

type modJobStatus struct {
	Phase   string `json:"phase"` // queued | downloading | installing | done | failed
	Package string `json:"package"`
	Version string `json:"version"`
	Done    int64  `json:"done"`
	Total   int64  `json:"total"`
	Error   string `json:"error,omitempty"`
	Folder  string `json:"folder,omitempty"` // where the mod landed
}

func (j *modJob) set(fn func(*modJobStatus)) {
	j.mu.Lock()
	fn(&j.st)
	j.mu.Unlock()
}

func (j *modJob) status() modJobStatus {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.st
}

// modInstall is a version of a Thunderstore package to install in dir.
type modInstall struct {
	target  string // an instance ID, or sharedTarget for the shared mods in dir
	dir     string
	pkg     *thunderstore.Package
	ver     *thunderstore.Version
	replace string // the folder of another version it takes the place of, or ""
}

// startModJob answers the id of a job that downloads and installs m, and
// writes the audit entry action once it is in.
func (a *API) startModJob(w http.ResponseWriter, r *http.Request, m modInstall, action string) {
	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		a.internalErr(w, r, err)
		return
	}
	a.sweepModUploads()
	j, ok := a.addModJob(r, m, "downloading")
	if !ok {
		writeErr(w, http.StatusConflict, m.pkg.FullName+" is already being installed there")
		return
	}
	audit := a.auditLater(r, auditInstance(m.target), action)
	a.goWork(func(ctx context.Context) { a.runModJob(ctx, j, m, audit) })
	writeJSON(w, http.StatusAccepted, map[string]any{"id": j.id, "version": m.ver.Number})
}

// addModJob registers a job for m that starts in phase, unless one for the
// same package and place is still running.
func (a *API) addModJob(r *http.Request, m modInstall, phase string) (*modJob, bool) {
	j := &modJob{jobBase: jobBase{id: rand.Text(), user: from(r).user.ID}, target: m.target, owner: isOwner(r), pkg: strings.ToLower(m.pkg.FullName),
		st: modJobStatus{Phase: phase, Package: m.pkg.FullName, Version: m.ver.Number, Total: m.ver.FileSize}}
	ok := a.modJobs.add(j, func(o *modJob) bool { return o.target == j.target && o.pkg == j.pkg })
	return j, ok
}

// runModJob downloads and installs m for j, then writes its audit entry.
func (a *API) runModJob(ctx context.Context, j *modJob, m modInstall, audit func(detail string)) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Hour)
	defer cancel()
	j.set(func(s *modJobStatus) { s.Phase = "downloading" })
	folder, err := func() (folder string, err error) {
		defer a.recovered("mod install", &err)
		return a.fetchMod(ctx, j, m)
	}()
	if err != nil {
		a.Log.Warn("mod install failed", "package", m.pkg.FullName, "version", m.ver.Number, "target", m.target, "err", err)
		audit(m.pkg.FullName + " " + m.ver.Number + " (failed: " + err.Error() + ")")
	} else if m.replace != "" {
		audit(m.replace + " -> " + folder)
	} else {
		audit(folder)
	}
	j.mu.Lock()
	j.finished = time.Now()
	if err != nil {
		j.st.Phase, j.st.Error = "failed", a.failText(j.owner, err)
	} else {
		j.st.Phase, j.st.Folder = "done", folder
	}
	j.mu.Unlock()
}

// auditInstance is the instance an audit entry about target names: none for
// the shared mods.
func auditInstance(target string) string {
	if target == sharedTarget {
		return ""
	}
	return target
}

// fetchMod downloads m and installs it, reporting how far it got to j.
func (a *API) fetchMod(ctx context.Context, j *modJob, m modInstall) (string, error) {
	f, err := os.CreateTemp(m.dir, ".mod-download-*.zip")
	if err != nil {
		return "", err
	}
	f.Close()
	defer os.Remove(f.Name())
	err = a.Thunderstore.Download(ctx, m.ver, f.Name(), maxModDownload, func(done, total int64) {
		j.set(func(s *modJobStatus) { s.Done, s.Total = done, max(total, done) })
	})
	if err != nil {
		return "", fmt.Errorf("download: %w", err)
	}
	j.set(func(s *modJobStatus) { s.Phase = "installing" })

	// Unpacked before taking the lock, which every other change to mods waits
	// on: a big map can take minutes.
	un, err := serverconfig.UnpackMod(m.dir, f.Name(), m.pkg.FullName+"-"+m.ver.Number+".zip")
	if err != nil {
		return "", err
	}
	defer un.Discard()
	shared := m.target == sharedTarget
	var servers []sharedServer
	if shared {
		servers = a.sharedServers()
	}
	a.modMu.Lock()
	defer a.modMu.Unlock()
	folder, _, err := un.Install(m.replace)
	if (err != nil && folder == "") || !shared {
		return folder, err
	}
	// Installed, if the old version wouldn't go: the links follow all the same,
	// so servers don't load both, and the job still says it failed.
	// Each server's link follows the shared mod to its new folder, enabled or
	// disabled as it was, and their own copies of this version give way to it.
	if m.replace != "" && folder != m.replace {
		for _, s := range servers {
			if err := serverconfig.RenameShared(s.dir, m.replace, folder, a.SharedDir); err != nil {
				a.Log.Warn("shared mods", "instance", s.id, "err", err)
			}
		}
	}
	a.adopt(servers, folder)
	a.relink(servers)
	return folder, err
}

// modJobStatus answers how a job the caller started, installing to target,
// is getting on.
func (a *API) modJobStatus(w http.ResponseWriter, r *http.Request, target string) {
	j, ok := a.modJobs.get(r)
	if !ok || j.target != target {
		writeErr(w, 404, "that install has finished or expired")
		return
	}
	writeJSON(w, 200, j.status())
}

func (a *API) installStatus(w http.ResponseWriter, r *http.Request) {
	a.modJobStatus(w, r, inst(r).ID())
}

func (a *API) sharedInstallStatus(w http.ResponseWriter, r *http.Request) {
	a.modJobStatus(w, r, sharedTarget)
}

// findPackage looks up a package on Thunderstore, and one of its versions:
// the newest when version is empty. On failure it has answered the request.
func (a *API) findPackage(w http.ResponseWriter, r *http.Request, name, version string) (*thunderstore.Package, *thunderstore.Version, bool) {
	if a.Thunderstore == nil {
		writeErr(w, 404, "Thunderstore is turned off")
		return nil, nil, false
	}
	pkgs, err := a.Thunderstore.Packages(r.Context(), false)
	if err != nil {
		a.fail(w, r, http.StatusBadGateway, fmt.Errorf("cannot reach Thunderstore: %w", err))
		return nil, nil, false
	}
	p := pkgs[strings.ToLower(name)]
	if name == "" || p == nil || p.Latest() == nil {
		writeErr(w, 404, "Thunderstore has no mod named "+name)
		return nil, nil, false
	}
	v := p.Latest()
	if version != "" {
		if v = p.Version(version); v == nil {
			writeErr(w, 404, fmt.Sprintf("%s has no version %s", p.FullName, version))
			return nil, nil, false
		}
	}
	return p, v, true
}

// installedAs finds the copy of package p among mods to install a version in
// place of. A copy of the same version, or one the shared mods load on this
// server, stops the install.
func installedAs(mods []serverconfig.Mod, p *thunderstore.Package, v *thunderstore.Version, where string) (replace string, err error) {
	for _, m := range mods {
		if m.Package == "" || !strings.EqualFold(m.Package, p.FullName) {
			continue
		}
		switch {
		case m.Shared && !m.Disabled:
			return "", fmt.Errorf("%s already loads %s from the shared mods", where, m.Title)
		case m.Shared:
			// Turned off here, so a copy of the server's own does not clash.
		case m.Version == v.Number:
			return "", fmt.Errorf("%s already has %s v%s", where, m.Title, v.Number)
		case replace == "":
			replace = m.Folder
		}
	}
	return replace, nil
}

// installMod installs a mod from Thunderstore on a server. Another version
// the server has of it is replaced, staying enabled or disabled as it was.
func (a *API) installMod(w http.ResponseWriter, r *http.Request) {
	in := inst(r)
	var req struct {
		Package string `json:"package"`
		Version string `json:"version"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	p, v, ok := a.findPackage(w, r, req.Package, req.Version)
	if !ok {
		return
	}
	dir := in.Def().Dir
	mods, err := serverconfig.ReadMods(dir)
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	replace, err := installedAs(mods, p, v, "this server")
	if err != nil {
		a.fail(w, r, http.StatusConflict, err)
		return
	}
	if folder := a.sharedCopy(in.Def(), p, v); folder != "" {
		a.loadSharedCopy(w, r, in, folder, replace, v.Number)
		return
	}
	a.startModJob(w, r, modInstall{target: in.ID(), dir: dir, pkg: p, ver: v, replace: replace}, "mod.install")
}

// sharedCopy is the folder of version v of package p in the shared mods, if
// the server uses them and they hold it.
func (a *API) sharedCopy(d instance.Def, p *thunderstore.Package, v *thunderstore.Version) string {
	if a.SharedDir == "" || d.SharedMods == serverconfig.SharedOff {
		return ""
	}
	mods, err := serverconfig.ReadMods(a.SharedDir)
	if err != nil {
		return ""
	}
	for _, m := range mods {
		if m.Package != "" && strings.EqualFold(m.Package, p.FullName) && m.Version == v.Number {
			return m.Folder
		}
	}
	return ""
}

// loadSharedCopy installs a version the shared mods hold on a server that
// uses them by loading it through its link, with nothing to download: the
// link is turned on, and the server's own copy of another version, replace,
// goes. It answers at once, where a download answers with a job.
func (a *API) loadSharedCopy(w http.ResponseWriter, r *http.Request, in *instance.Instance, folder, replace, version string) {
	d := in.Def()
	a.modMu.Lock()
	var err error
	if replace != "" {
		err = serverconfig.DeleteMod(d.Dir, replace)
	}
	if err == nil {
		err = serverconfig.SyncShared(d.Dir, a.SharedDir, d.SharedMods)
	}
	if err == nil {
		err = serverconfig.SetModEnabled(d.Dir, folder, true)
	}
	a.modMu.Unlock()
	if err != nil {
		a.modErr(w, r, err)
		return
	}
	detail := folder + " (shared)"
	if replace != "" {
		detail = replace + " -> " + detail
	}
	a.audit(r, d.ID, "mod.install", detail)
	writeJSON(w, 200, map[string]any{"folder": folder, "version": version, "linked": true})
}

// installSharedMod adds a mod from Thunderstore to the shared mods, or swaps
// the version they hold. It reaches every server that uses them; one that
// picks its shared mods gets it turned off, until an install there turns it on.
func (a *API) installSharedMod(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Package string `json:"package"`
		Version string `json:"version"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	p, v, ok := a.findPackage(w, r, req.Package, req.Version)
	if !ok {
		return
	}
	mods, err := serverconfig.ReadMods(a.SharedDir)
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	replace, err := installedAs(mods, p, v, "the shared mods")
	if err != nil {
		a.fail(w, r, http.StatusConflict, err)
		return
	}
	a.startModJob(w, r, modInstall{target: sharedTarget, dir: a.SharedDir, pkg: p, ver: v, replace: replace}, "shared.install")
}

// updateMod installs the newest version from Thunderstore of the mod in
// {folder} on a server, in its place.
func (a *API) updateMod(w http.ResponseWriter, r *http.Request) {
	in := inst(r)
	a.startModUpdate(w, r, in.ID(), in.Def().Dir, "mod.update")
}

// updateSharedMod installs a shared mod's newest version from Thunderstore.
// Each server's link follows it to its new folder, enabled or disabled as it
// was.
func (a *API) updateSharedMod(w http.ResponseWriter, r *http.Request) {
	a.startModUpdate(w, r, sharedTarget, a.SharedDir, "shared.update")
}

// startModUpdate starts installing the newest version of the mod in {folder},
// in dir, in its place.
func (a *API) startModUpdate(w http.ResponseWriter, r *http.Request, target, dir, action string) {
	folder, ok := modFolder(w, r)
	if !ok {
		return
	}
	mods, err := serverconfig.ReadMods(dir)
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	var mod *serverconfig.Mod
	for i := range mods {
		if mods[i].Folder == folder {
			mod = &mods[i]
		}
	}
	if mod == nil {
		writeErr(w, 404, "that mod is not installed")
		return
	}
	if mod.Shared {
		a.modErr(w, r, errShared)
		return
	}
	if mod.Package == "" {
		writeErr(w, 404, "this mod is not on Thunderstore")
		return
	}
	p, v, ok := a.findPackage(w, r, mod.Package, "")
	if !ok {
		return
	}
	if mod.Version != "" && !serverconfig.NewerVersion(v.Number, mod.Version) {
		writeErr(w, 409, mod.Title+" is already up to date")
		return
	}
	a.startModJob(w, r, modInstall{target: target, dir: dir, pkg: p, ver: v, replace: folder}, action)
}

// updateAllMods installs the newest version from Thunderstore of each of a
// server's mods that has a newer one. Mods linked from the shared mods are
// theirs to update.
func (a *API) updateAllMods(w http.ResponseWriter, r *http.Request) {
	in := inst(r)
	a.startUpdateAll(w, r, in.ID(), in.Def().Dir, "mod.update")
}

// updateAllSharedMods does the same for the shared mods.
func (a *API) updateAllSharedMods(w http.ResponseWriter, r *http.Request) {
	a.startUpdateAll(w, r, sharedTarget, a.SharedDir, "shared.update")
}

// updateJob is one of the jobs an update of every mod started.
type updateJob struct {
	ID      string `json:"id"`
	Folder  string `json:"folder"`
	Title   string `json:"title"`
	Version string `json:"version"`
}

// startUpdateAll starts a job for each mod in dir with a newer version on
// Thunderstore, each queued behind the one before: one big map downloads at a
// time, and the updates carry on with the panel closed. A mod already being
// installed there is left out, and counted as running. It answers the jobs in
// the order they run, none when everything is up to date or under way.
func (a *API) startUpdateAll(w http.ResponseWriter, r *http.Request, target, dir, action string) {
	if a.Thunderstore == nil {
		writeErr(w, 404, "Thunderstore is turned off")
		return
	}
	mods, err := serverconfig.ReadMods(dir)
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	pkgs, err := a.Thunderstore.Packages(r.Context(), false)
	if err != nil {
		a.fail(w, r, http.StatusBadGateway, fmt.Errorf("cannot reach Thunderstore: %w", err))
		return
	}
	a.sweepModUploads()
	type queued struct {
		job    *modJob
		m      modInstall
		audit  func(string)
		answer updateJob
	}
	var queue []queued
	running := 0
	for _, mod := range mods {
		p := pkgs[strings.ToLower(mod.Package)]
		if mod.Shared || mod.Package == "" || p == nil || p.Latest() == nil || !serverconfig.NewerVersion(p.Latest().Number, mod.Version) {
			continue
		}
		m := modInstall{target: target, dir: dir, pkg: p, ver: p.Latest(), replace: mod.Folder}
		if j, ok := a.addModJob(r, m, "queued"); ok {
			queue = append(queue, queued{j, m, a.auditLater(r, auditInstance(target), action), updateJob{j.id, mod.Folder, mod.Title, m.ver.Number}})
		} else {
			running++
		}
	}
	a.goWork(func(ctx context.Context) {
		for _, q := range queue {
			a.runModJob(ctx, q.job, q.m, q.audit)
		}
	})
	jobs := []updateJob{}
	for _, q := range queue {
		jobs = append(jobs, q.answer)
	}
	code := http.StatusAccepted
	if len(jobs) == 0 {
		code = http.StatusOK
	}
	writeJSON(w, code, map[string]any{"jobs": jobs, "running": running})
}
