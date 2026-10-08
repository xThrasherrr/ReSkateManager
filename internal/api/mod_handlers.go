package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/xThrasherrr/ReSkateManager/internal/serverconfig"
)

// modUpdate is what Thunderstore knows about an installed mod.
type modUpdate struct {
	Package string `json:"package"`
	URL     string `json:"url"`
	Latest  string `json:"latest"`
	Newer   bool   `json:"newer"` // Latest is newer than the installed version
}

// modUpdates matches the server's mods against Thunderstore, by folder. Mods
// that are not on Thunderstore are left out.
func (a *API) modUpdates(w http.ResponseWriter, r *http.Request) {
	a.writeModUpdates(w, r, inst(r).Def().Dir)
}

// writeModUpdates answers what Thunderstore knows about the mods in dir.
func (a *API) writeModUpdates(w http.ResponseWriter, r *http.Request, dir string) {
	if a.Thunderstore == nil {
		writeJSON(w, 200, map[string]modUpdate{})
		return
	}
	mods, err := serverconfig.ReadMods(dir)
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	pkgs, err := a.Thunderstore.Packages(r.Context(), r.URL.Query().Has("refresh"))
	if err != nil {
		a.fail(w, r, http.StatusBadGateway, fmt.Errorf("cannot reach Thunderstore: %w", err))
		return
	}
	out := map[string]modUpdate{}
	for _, m := range mods {
		p := pkgs[strings.ToLower(m.Package)]
		if m.Package == "" || p == nil || p.Latest() == nil {
			continue
		}
		latest := p.Latest().Number
		out[m.Folder] = modUpdate{Package: p.FullName, URL: p.URL, Latest: latest, Newer: serverconfig.NewerVersion(latest, m.Version)}
	}
	writeJSON(w, 200, out)
}

// modFolder is the {folder} in the path, which names a mod in Mods or DisabledMods.
func modFolder(w http.ResponseWriter, r *http.Request) (string, bool) {
	folder, err := pathParam(r, "folder")
	if err != nil || folder == "" {
		writeErr(w, 400, "bad mod folder")
		return "", false
	}
	return folder, true
}

func (a *API) modErr(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, serverconfig.ErrNoMod) {
		writeErr(w, 404, "that mod is not installed")
		return
	}
	a.fail(w, r, 400, err)
}

// errShared turns away a change to a shared mod made from one server.
var errShared = errors.New("this mod comes from the shared mods; update or delete it there, or disable it on this server")

// setModEnabled enables or disables a mod. The server reads Mods only when it
// starts, so a running one keeps the mod's maps until it restarts.
func (a *API) setModEnabled(w http.ResponseWriter, r *http.Request) {
	in := inst(r)
	folder, ok := modFolder(w, r)
	if !ok {
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	dir := in.Def().Dir
	a.modMu.Lock()
	err := serverconfig.SetModEnabled(dir, folder, req.Enabled)
	a.modMu.Unlock()
	if err != nil {
		a.modErr(w, r, err)
		return
	}
	action := "mod.disable"
	if req.Enabled {
		action = "mod.enable"
	}
	a.audit(r, in.ID(), action, folder)
	writeJSON(w, 200, map[string]any{"folder": folder, "enabled": req.Enabled})
}

// deleteMod deletes a server's own mod. Its shared copy, if there is one and
// the server uses the shared mods, takes its place.
func (a *API) deleteMod(w http.ResponseWriter, r *http.Request) {
	in := inst(r)
	d := in.Def()
	folder, ok := modFolder(w, r)
	if !ok {
		return
	}
	a.modMu.Lock()
	err := errShared
	if !serverconfig.IsShared(d.Dir, folder) {
		err = serverconfig.DeleteMod(d.Dir, folder)
	}
	if err == nil && d.SharedMods != serverconfig.SharedOff && a.SharedDir != "" {
		if lerr := serverconfig.SyncShared(d.Dir, a.SharedDir, d.SharedMods); lerr != nil {
			a.Log.Warn("shared mods", "instance", d.ID, "err", lerr)
		}
	}
	a.modMu.Unlock()
	if err != nil {
		a.modErr(w, r, err)
		return
	}
	a.audit(r, in.ID(), "mod.delete", folder)
	writeJSON(w, 200, map[string]any{"folder": folder})
}
