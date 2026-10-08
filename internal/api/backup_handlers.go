package api

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/announce"
	"github.com/xThrasherrr/ReSkateManager/internal/auth"
	"github.com/xThrasherrr/ReSkateManager/internal/backup"
	"github.com/xThrasherrr/ReSkateManager/internal/instance"
	"github.com/xThrasherrr/ReSkateManager/internal/serverconfig"
	"github.com/xThrasherrr/ReSkateManager/internal/store"
)

var errNoBackups = errors.New("backups are not set up in this manager")

// ---- the backups (owners: they hold the database, with password hashes) ----

func (a *API) backups(w http.ResponseWriter, r *http.Request) {
	if a.Backups == nil {
		writeErr(w, 404, errNoBackups.Error())
		return
	}
	list, err := a.Backups.List()
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	set, err := backup.LoadSettings(r.Context(), a.Store)
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"backups": list, "settings": set, "folder": a.Backups.Dir})
}

// createBackup backs up the database and every server, as a job.
func (a *API) createBackup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mods bool `json:"mods"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if a.Backups == nil {
		writeErr(w, 404, errNoBackups.Error())
		return
	}
	audit := a.auditLater(r, "", "backup.create")
	opt := backup.Options{Kind: backup.Manual, By: from(r).user.Username, Database: true, Servers: a.Reg.IDs(), Mods: req.Mods}
	a.startJob(w, r, func(ctx context.Context, progress func(done, total int64)) (any, error) {
		opt.Progress = progress
		info, err := a.Backups.Create(ctx, opt)
		if err != nil {
			a.Log.Warn("backup failed", "err", err)
			return nil, err
		}
		audit(info.Name)
		return info, nil
	})
}

func (a *API) saveBackupSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Every *int  `json:"every"`
		Keep  *int  `json:"keep"`
		Mods  *bool `json:"mods"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	a.cfgMu.Lock() // so two saves at once can't each keep only their own change
	defer a.cfgMu.Unlock()
	set, err := backup.LoadSettings(r.Context(), a.Store)
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	if req.Every != nil {
		set.Every = *req.Every
	}
	if req.Keep != nil {
		set.Keep = *req.Keep
	}
	if req.Mods != nil {
		set.Mods = *req.Mods
	}
	if err := backup.SaveSettings(r.Context(), a.Store, set); err != nil {
		a.fail(w, r, 400, err)
		return
	}
	a.audit(r, "", "backup.settings", fmt.Sprintf("every %d hours, keep %d, mods %v", set.Every, set.Keep, set.Mods))
	writeJSON(w, 200, set)
}

func (a *API) downloadBackup(w http.ResponseWriter, r *http.Request) {
	if a.Backups == nil {
		writeErr(w, 404, errNoBackups.Error())
		return
	}
	name, _ := pathParam(r, "name") // a name the backups never use is refused below
	f, err := a.Backups.Open(name)
	if err != nil {
		a.fail(w, r, 404, err)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	if wholeDownload(r) {
		a.audit(r, "", "backup.download", name)
	}
	w.Header().Set("Content-Type", "application/zip")
	attachment(w, name)
	http.ServeContent(w, r, name, fi.ModTime(), f)
}

func (a *API) deleteBackup(w http.ResponseWriter, r *http.Request) {
	if a.Backups == nil {
		writeErr(w, 404, errNoBackups.Error())
		return
	}
	name, _ := pathParam(r, "name") // a name the backups never use is refused below
	if err := a.Backups.Delete(name); errors.Is(err, backup.ErrNoBackup) {
		a.fail(w, r, 404, err)
		return
	} else if err != nil {
		a.internalErr(w, r, err)
		return
	}
	a.audit(r, "", "backup.delete", name)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---- one server ----

// serverBackup is a backup that holds a server, as its Server page lists it.
type serverBackup struct {
	Name     string       `json:"name"`
	Kind     backup.Kind  `json:"kind"`
	Created  int64        `json:"created"`
	Version  string       `json:"version"`
	By       string       `json:"by,omitempty"`
	Size     int64        `json:"size"`
	Files    []string     `json:"files"`
	Mods     []backup.Mod `json:"mods"`
	ModFiles bool         `json:"modFiles"`
}

func (a *API) serverBackups(w http.ResponseWriter, r *http.Request) {
	if a.Backups == nil {
		writeErr(w, 404, errNoBackups.Error())
		return
	}
	list, err := a.Backups.List()
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	id := inst(r).ID()
	out := []serverBackup{}
	for _, b := range list {
		i := slices.IndexFunc(b.Servers, func(s backup.Server) bool { return s.ID == id })
		if i < 0 {
			continue
		}
		s := b.Servers[i]
		out = append(out, serverBackup{b.Name, b.Kind, b.Created, b.Version, b.By, b.Size, s.Files, s.Mods, s.ModFiles})
	}
	writeJSON(w, 200, out)
}

// restoreServer puts a stopped server's config, and its mods if asked, back
// from a backup, as a job. The server stays in Updating meanwhile, so nothing
// starts it half restored.
func (a *API) restoreServer(w http.ResponseWriter, r *http.Request) {
	in := inst(r)
	var req struct {
		Backup string `json:"backup"`
		Mods   bool   `json:"mods"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if a.Backups == nil {
		writeErr(w, 404, errNoBackups.Error())
		return
	}
	p, err := a.Backups.Path(req.Backup)
	if err != nil {
		a.fail(w, r, 404, err)
		return
	}
	rd, err := backup.OpenFile(p)
	if err != nil {
		a.fail(w, r, 400, fmt.Errorf("that backup can't be read: %w", err))
		return
	}
	srv, ok := rd.Server(in.ID())
	switch {
	case !ok:
		err = errors.New("that backup doesn't hold this server")
	case req.Mods && !srv.ModFiles:
		err = errors.New("that backup doesn't hold this server's mods")
	}
	if err != nil {
		rd.Close()
		a.fail(w, r, 400, err)
		return
	}
	if err := in.BeginUpdate(); err != nil {
		rd.Close()
		writeErr(w, 409, "stop the server before restoring it")
		return
	}
	// Bringing back missing shared mods changes every server that uses them.
	shared := req.Mods && from(r).perms.Can(auth.SettingsEdit, "")
	perms := from(r).perms
	audit := a.auditLater(r, in.ID(), "server.restore")
	a.startJob(w, r, func(ctx context.Context, progress func(done, total int64)) (_ any, err error) {
		defer rd.Close()
		defer in.EndUpdate()
		// A failed restore may have put some files back already.
		defer func() {
			if err != nil {
				audit(req.Backup + " (failed: " + err.Error() + ")")
			}
		}()
		def := in.Def()
		old, _ := serverconfig.Read(def.ConfigPath())
		files, err := rd.RestoreConfig(in.ID(), def.Dir)
		if err != nil {
			return nil, err
		}
		if !perms.Owner {
			if err := keepUnheld(def.ConfigPath(), old, perms, in.ID()); err != nil {
				return nil, err
			}
		}
		res := map[string]any{"files": files}
		detail := req.Backup + ": " + strings.Join(files, ", ")
		if req.Mods {
			n, err := rd.RestoreMods(ctx, in.ID(), def.Dir, progress)
			if err != nil {
				return nil, err
			}
			res["mods"] = n
			detail += fmt.Sprintf(", %d mods", n)
			if shared && def.SharedMods != serverconfig.SharedOff {
				back, err := rd.RestoreShared(ctx, a.SharedDir, progress)
				if err != nil {
					return nil, fmt.Errorf("restored the server's mods, but not the shared ones: %w", err)
				}
				res["shared"] = back
				if len(back) > 0 {
					detail += ", shared " + strings.Join(back, ", ")
				}
			}
			if err := a.LinkSharedMods(def); err != nil {
				a.Log.Warn("link the shared mods after a restore", "instance", in.ID(), "err", err)
			}
		}
		audit(detail)
		in.Note("Restored from backup " + req.Backup + ".")
		return res, nil
	})
}

// exportServer answers a zip of the server's config, announcements and,
// unless ?mods=0, every mod it loads, to import on another manager.
func (a *API) exportServer(w http.ResponseWriter, r *http.Request) {
	in := inst(r)
	def := in.Def()
	mods := r.URL.Query().Get("mods") != "0"
	all, err := a.Store.Announcements(r.Context(), in.ID())
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	// Those for every server stay; they belong to this manager.
	own := slices.DeleteFunc(all, func(x store.Announcement) bool { return x.Instance != in.ID() })
	detail := "with mods"
	if !mods {
		detail = "without mods"
	}
	name := fmt.Sprintf("%s-export-%s.zip", def.ID, time.Now().Format("20060102"))
	w.Header().Set("Content-Type", "application/zip")
	attachment(w, name)
	cw := &countingWriter{w: w}
	err = backup.WriteExport(r.Context(), cw, def, mods, !from(r).perms.Owner, own, a.Version)
	a.auditOutcome(r, in.ID(), "instance.export", detail, err)
	if err != nil {
		if cw.n == 0 {
			w.Header().Del("Content-Disposition")
			a.internalErr(w, r, err)
			return
		}
		// Too late for an error; the download ends short.
		a.Log.Warn("export cut short", "instance", in.ID(), "err", err)
	}
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// ---- importing a server ----

// An export with its mods can be big, and is sent in chunks like a mod.
const maxImport = 16 << 30

// importTarget is the target of uploads of servers to import; no instance ID
// can be it.
const importTarget = "*import"

func (a *API) startImport(w http.ResponseWriter, r *http.Request) {
	if a.ServersDir == "" {
		writeErr(w, 400, "this manager has no servers folder to import into")
		return
	}
	a.startUpload(w, r, importTarget, a.ServersDir, maxImport)
}

// importChunk takes a chunk of an export being imported and, once it's all
// in, adds the server it holds as a job.
func (a *API) importChunk(w http.ResponseWriter, r *http.Request) {
	a.uploadChunk(w, r, importTarget, func(upload, _ string) {
		// Renamed, so the manager's next start knows it for an import's.
		zipPath := filepath.Join(a.ServersDir, ".import-"+rand.Text()+".zip")
		if err := os.Rename(upload, zipPath); err != nil {
			os.Remove(upload)
			a.internalErr(w, r, err)
			return
		}
		rd, err := backup.OpenFile(zipPath)
		if err == nil && len(rd.Servers) != 1 {
			rd.Close()
			err = fmt.Errorf("it holds %d servers; export the one server to move from its Server page", len(rd.Servers))
		}
		if err != nil {
			os.Remove(zipPath)
			a.fail(w, r, 400, fmt.Errorf("that zip can't be imported: %w", err))
			return
		}
		perms := from(r).perms
		audit := a.auditLaterFor(r, "instance.import")
		a.startJob(w, r, func(ctx context.Context, progress func(done, total int64)) (any, error) {
			defer os.Remove(zipPath)
			defer rd.Close()
			d, notes, err := a.importServer(ctx, rd, perms, progress)
			if err != nil {
				a.Log.Warn("import failed", "err", err)
				audit("", rd.Servers[0].Name+" (failed: "+err.Error()+")")
				return nil, err
			}
			audit(d.ID, d.Dir)
			return map[string]any{"id": d.ID, "name": d.Name, "notes": notes}, nil
		})
	})
}

// importServer adds the server an export holds as a new one, in its own
// folder in servers/, with its settings and announcements. Its owner-only
// settings and in-game admins come along only for someone who could set them.
func (a *API) importServer(ctx context.Context, rd *backup.Reader, perms auth.Perms, progress func(done, total int64)) (instance.Def, []string, error) {
	src := rd.Servers[0]
	name, err := serverName(src.Name)
	if err != nil {
		name = "Imported server"
	}
	id, err := a.reserveID(name)
	if err != nil {
		return instance.Def{}, nil, err
	}
	defer a.releaseID(id)
	d := instance.Def{ID: id, Name: name, Dir: filepath.Join(a.ServersDir, id),
		AutoStart: src.AutoStart, AutoRestart: src.AutoRestart, AutoUpdate: src.AutoUpdate, SharedMods: src.SharedMods}
	if times, err := instance.ParseRestartTimes(src.RestartTimes); err == nil {
		d.RestartTimes = times
	}
	if src.RestartHours > 0 && src.RestartHours <= instance.MaxRestartHours {
		d.RestartHours = src.RestartHours
	}
	if a.SharedDir == "" {
		d.SharedMods = serverconfig.SharedOff
	}
	if _, err := rd.ImportServer(ctx, d.Dir, progress); err != nil {
		return d, nil, err
	}
	if !perms.Owner {
		if err := keepUnheld(d.ConfigPath(), nil, perms, id); err != nil {
			os.RemoveAll(d.Dir)
			return d, nil, err
		}
	}
	var notes []string
	if note, err := a.freeImportPorts(d.ConfigPath()); err != nil {
		a.Log.Warn("ports of an imported server", "err", err)
	} else if note != "" {
		notes = append(notes, note)
	}
	if err := a.Store.CreateInstance(ctx, d); err != nil {
		os.RemoveAll(d.Dir)
		return d, nil, err
	}
	for _, an := range src.Announcements {
		msg := cleanText(an.Message)
		if msg == "" || len(msg) > 200 || !announce.ValidInterval(an.Interval) {
			continue
		}
		next := store.Announcement{Instance: id, Message: msg, Interval: an.Interval, Enabled: an.Enabled}
		if err := a.Store.SaveAnnouncement(ctx, &next); err != nil {
			a.Log.Warn("announcement of an imported server", "err", err)
		}
	}
	a.Reg.Add(d)
	a.Admins.Kick()
	if _, err := os.Stat(d.Exe()); err != nil {
		notes = append(notes, "Its server files aren't included; install them from its Updates page.")
	}
	return d, notes, nil
}

// reserveID picks an ID for a server being imported and holds it until
// releaseID: one no server has, no other import holds, and whose folder in
// servers/ doesn't exist (nor a kept folder of a server removed earlier).
func (a *API) reserveID(name string) (string, error) {
	a.instMu.Lock()
	defer a.instMu.Unlock()
	id := slugify(name)
	for i := 2; ; i++ {
		_, taken := a.Reg.Get(id)
		_, err := os.Lstat(filepath.Join(a.ServersDir, id))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		if !taken && !a.reserved[id] && err != nil {
			break
		}
		if i > 1000 {
			return "", errors.New("no free folder name for it in the servers folder")
		}
		id = fmt.Sprintf("%s-%d", slugify(name), i)
	}
	if a.reserved == nil {
		a.reserved = map[string]bool{}
	}
	a.reserved[id] = true
	return id, nil
}

func (a *API) releaseID(id string) {
	a.instMu.Lock()
	delete(a.reserved, id)
	a.instMu.Unlock()
}

// keepUnheld undoes what a config someone who isn't an owner restored or
// imported would change that they may not change by hand: the owner-only
// settings, the in-game admins (who can run any console command) and, on a
// restore, the bans. old is the config before a restore, nil for an import,
// which then brings none of those. A file that can't be read is left alone:
// the server refuses it too.
func keepUnheld(path string, old *serverconfig.File, perms auth.Perms, instanceID string) error {
	f, err := serverconfig.Read(path)
	if err != nil {
		return nil
	}
	f.KeepOwnerOnly(old)
	var admins []string
	if old != nil {
		admins = old.Admins()
	}
	if !perms.Can(auth.IngameAdmins, instanceID) {
		f.SetAdmins(admins)
	}
	if old != nil && !perms.Can(auth.PlayersBan, instanceID) {
		f.SetBans(old.Bans())
	}
	return f.Write(path)
}

// freeImportPorts moves an imported server's config to free ports when
// another server here uses either of its own, which would keep one of the
// two from starting.
func (a *API) freeImportPorts(cfgPath string) (string, error) {
	f, err := serverconfig.Read(cfgPath)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	used := a.usedPorts()
	v := f.Values()
	port, _ := v["port"].(float64)
	query, _ := v["query_port"].(float64)
	if !used[int(port)] && !used[int(query)] {
		return "", nil
	}
	p, q, ok := a.freePorts()
	if !ok {
		return "Its ports are in use here and no free pair was found; give it new ones in its settings before starting it.", nil
	}
	f.Set("port", p)
	f.Set("query_port", q)
	if err := f.Write(cfgPath); err != nil {
		return "", err
	}
	return fmt.Sprintf("Its ports were in use here, so it now uses %d and %d.", p, q), nil
}
