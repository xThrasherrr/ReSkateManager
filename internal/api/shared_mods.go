package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/xThrasherrr/ReSkateManager/internal/auth"
	"github.com/xThrasherrr/ReSkateManager/internal/instance"
	"github.com/xThrasherrr/ReSkateManager/internal/serverconfig"
)

// sharedServer is what the shared mods need of a server, read before taking
// modMu.
type sharedServer struct {
	id, name, dir string
	use           serverconfig.SharedUse
}

func (a *API) sharedServers() []sharedServer {
	var out []sharedServer
	for _, in := range a.Reg.List() {
		d := in.Def()
		out = append(out, sharedServer{d.ID, d.Name, d.Dir, d.SharedMods})
	}
	return out
}

// relink brings each server's links to the shared mods up to date. The
// caller holds modMu.
func (a *API) relink(servers []sharedServer) {
	for _, s := range servers {
		if err := serverconfig.SyncShared(s.dir, a.SharedDir, s.use); err != nil {
			a.Log.Warn("shared mods", "instance", s.id, "err", err)
		}
	}
}

// adopt swaps the copies of shared mod folder on servers that use the shared
// mods for links, where they are the same version, and names those servers.
// The caller holds modMu.
func (a *API) adopt(servers []sharedServer, folder string) []string {
	swapped := []string{}
	for _, s := range servers {
		if s.use == serverconfig.SharedOff {
			continue
		}
		ok, err := serverconfig.UseShared(s.dir, folder, a.SharedDir)
		if err != nil {
			a.Log.Warn("shared mods", "instance", s.id, "err", err)
		}
		if ok {
			swapped = append(swapped, s.name)
		}
	}
	return swapped
}

// sharedFolders lists the shared mods' folders.
func (a *API) sharedFolders() map[string]bool {
	out := map[string]bool{}
	entries, _ := os.ReadDir(serverconfig.ModsDir(a.SharedDir))
	for _, e := range entries {
		if e.IsDir() {
			out[e.Name()] = true
		}
	}
	return out
}

// LinkSharedMods brings one server's links to the shared mods up to date.
// The manager runs it as each server starts, under the server's lock, so it
// leaves modMu be: a big mod being unpacked must not hold up the start, or
// the panel reading the server meanwhile. SyncShared copes with the overlap.
func (a *API) LinkSharedMods(d instance.Def) error {
	if a.SharedDir == "" {
		return nil
	}
	return serverconfig.SyncShared(d.Dir, a.SharedDir, d.SharedMods)
}

// RelinkSharedMods brings every server's links to the shared mods up to
// date, for the manager's start: its folder may have moved since.
func (a *API) RelinkSharedMods() {
	if a.SharedDir == "" {
		return
	}
	servers := a.sharedServers()
	a.modMu.Lock()
	a.relink(servers)
	a.modMu.Unlock()
}

var errSharedOff = errors.New("shared mods are turned off")

// sharedMods lists the shared mods, and for each server the caller may see,
// whether it uses them and how.
func (a *API) sharedMods(w http.ResponseWriter, r *http.Request) {
	mods, err := serverconfig.ReadMods(a.SharedDir)
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	type server struct {
		ID      string                 `json:"id"`
		Name    string                 `json:"name"`
		Use     serverconfig.SharedUse `json:"use"`
		CanEdit bool                   `json:"canEdit"` // may change Use, and enable or disable its shared mods
		Map     any                    `json:"map"`     // its map setting, so deleting a mod can warn
		Pool    []string               `json:"pool"`    // and its map pool
		// Shared mods it loads, ones turned off there, and ones it keeps its own
		// copy of. One in none of them is linked when the server next starts.
		Enabled  []string `json:"enabled"`
		Disabled []string `json:"disabled"`
		Own      []string `json:"own"`
	}
	shared := a.sharedFolders()
	perms := from(r).perms
	servers := []server{}
	for _, in := range a.Reg.List() {
		d := in.Def()
		if !perms.Can(auth.SettingsView, d.ID) {
			continue
		}
		s := server{ID: d.ID, Name: d.Name, Use: d.SharedMods, CanEdit: perms.Can(auth.SettingsEdit, d.ID), Enabled: []string{}, Disabled: []string{}, Own: []string{}}
		if f, err := serverconfig.Read(d.ConfigPath()); err == nil {
			s.Map, _ = f.Get("map")
			if p, ok := f.Values()["map_pool"].([]string); ok {
				s.Pool = p
			}
		}
		if d.SharedMods != serverconfig.SharedOff {
			own, _ := serverconfig.ReadMods(d.Dir)
			for _, m := range own {
				switch {
				case m.Shared && m.Disabled:
					s.Disabled = append(s.Disabled, m.Folder)
				case m.Shared:
					s.Enabled = append(s.Enabled, m.Folder)
				case !m.Shared && shared[m.Folder]:
					s.Own = append(s.Own, m.Folder)
				}
			}
		}
		servers = append(servers, s)
	}
	writeJSON(w, 200, map[string]any{"folder": serverconfig.ModsDir(a.SharedDir), "mods": mods, "servers": servers})
}

func (a *API) sharedModUpdates(w http.ResponseWriter, r *http.Request) {
	a.writeModUpdates(w, r, a.SharedDir)
}

func (a *API) startSharedModUpload(w http.ResponseWriter, r *http.Request) {
	a.startUpload(w, r, sharedTarget, a.SharedDir, maxModUpload)
}

// sharedModUploadChunk takes a chunk of an upload to the shared mods. Once
// the zip is in, it is installed and linked into every server that uses the
// shared mods; their own copies of the same version give way to it.
func (a *API) sharedModUploadChunk(w http.ResponseWriter, r *http.Request) {
	audit := a.auditLater(r, "", "shared.upload")
	a.uploadChunk(w, r, sharedTarget, func(zipPath, name string) {
		a.startJob(w, r, func(ctx context.Context, progress func(done, total int64)) (any, error) {
			defer os.Remove(zipPath)
			un, err := serverconfig.UnpackMod(a.SharedDir, zipPath, name)
			if err != nil {
				return nil, err
			}
			defer un.Discard()
			servers := a.sharedServers()
			a.modMu.Lock()
			folder, replaced, err := un.Install("")
			var swapped []string
			if err == nil {
				swapped = a.adopt(servers, folder)
				a.relink(servers)
			}
			a.modMu.Unlock()
			if err != nil {
				return nil, err
			}
			audit(folder)
			return map[string]any{"folder": folder, "replaced": replaced, "swapped": swapped}, nil
		})
	})
}

// deleteSharedMod deletes a shared mod and its link in every server.
func (a *API) deleteSharedMod(w http.ResponseWriter, r *http.Request) {
	folder, ok := modFolder(w, r)
	if !ok {
		return
	}
	servers := a.sharedServers()
	a.modMu.Lock()
	err := serverconfig.DeleteMod(a.SharedDir, folder)
	if err == nil {
		a.relink(servers)
	}
	a.modMu.Unlock()
	if err != nil {
		a.modErr(w, r, err)
		return
	}
	a.audit(r, "", "shared.delete", folder)
	writeJSON(w, 200, map[string]any{"folder": folder})
}

// setSharedMods sets how a server uses the shared mods. Starting to use them
// swaps the server's own copies of the same versions for links. Switching
// between all and pick leaves each mod enabled or disabled as it is; it
// changes only how mods shared later arrive.
func (a *API) setSharedMods(w http.ResponseWriter, r *http.Request) {
	in := inst(r)
	var req struct {
		Use serverconfig.SharedUse `json:"use"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	a.instMu.Lock()
	defer a.instMu.Unlock()
	d := in.Def()
	was := d.SharedMods
	d.SharedMods = req.Use
	if err := a.Store.UpdateInstance(r.Context(), d); err != nil {
		a.fail(w, r, errStatus(err), err)
		return
	}
	in.SetDef(d)
	a.modMu.Lock()
	swapped := []string{}
	if was == serverconfig.SharedOff && d.SharedMods != serverconfig.SharedOff {
		for folder := range a.sharedFolders() {
			if ok, err := serverconfig.UseShared(d.Dir, folder, a.SharedDir); err != nil {
				a.Log.Warn("shared mods", "instance", d.ID, "err", err)
			} else if ok {
				swapped = append(swapped, folder)
			}
		}
	}
	err := serverconfig.SyncShared(d.Dir, a.SharedDir, d.SharedMods)
	a.modMu.Unlock()
	use, _ := d.SharedMods.MarshalText()
	a.audit(r, d.ID, "mods.shared", string(use))
	if err != nil {
		a.fail(w, r, 500, fmt.Errorf("saved, but the shared mods could not all be linked: %w", err))
		return
	}
	writeJSON(w, 200, map[string]any{"use": d.SharedMods, "swapped": swapped})
}

// shareMod moves a server's own mod into the shared mods, for every server
// that uses them. Other servers' copies of the same version give way to it.
func (a *API) shareMod(w http.ResponseWriter, r *http.Request) {
	in := inst(r)
	d := in.Def()
	folder, ok := modFolder(w, r)
	if !ok {
		return
	}
	if d.SharedMods == serverconfig.SharedOff {
		writeErr(w, 400, "turn on the shared mods for this server first")
		return
	}
	servers := a.sharedServers()
	a.modMu.Lock()
	err := serverconfig.ShareMod(d.Dir, folder, a.SharedDir)
	var swapped []string
	if err == nil {
		swapped = a.adopt(servers, folder)
		a.relink(servers)
	}
	a.modMu.Unlock()
	if err != nil {
		a.modErr(w, r, err)
		return
	}
	a.audit(r, d.ID, "mod.share", folder)
	writeJSON(w, 200, map[string]any{"folder": folder, "swapped": swapped})
}
