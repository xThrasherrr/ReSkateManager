package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/instance"
	"github.com/xThrasherrr/ReSkateManager/internal/logparse"
	"github.com/xThrasherrr/ReSkateManager/internal/parks"
	"github.com/xThrasherrr/ReSkateManager/internal/serverconfig"
)

// The Parks page: parks built in the game's Park Editor and shared as mods
// (ReSkate 2.0.6). The server spawns them; the manager also keeps pools of
// them that go up as their map loads, and who may spawn them from chat.

// parkSetup is the park mods the server has set up, and how many parks it
// spawns at once.
func parkSetup(f *serverconfig.File) ([]serverconfig.ParkMod, int) {
	vals := f.Values()
	setup, _ := vals["park_mods"].([]serverconfig.ParkMod)
	limit, _ := vals["park_limits.parks"].(float64)
	return setup, int(limit)
}

// parks is everything the Parks page shows: the parks in the server's mods,
// the ones set up and the ones up, the pools, and who may use /parks.
func (a *API) parks(w http.ResponseWriter, r *http.Request) {
	in := inst(r)
	f, ok := readConfig(w, in)
	if !ok {
		return
	}
	found, err := serverconfig.ReadParks(in.Def().Dir)
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	set, err := parks.Load(r.Context(), a.Store, in.ID())
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	setup, limit := parkSetup(f)
	objects, _ := f.Values()["park_limits.objects"].(float64)
	var spawned []logparse.SpawnedPark // nil: not running, or a server that can't say
	if in.State() == instance.Running {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		spawned, _ = in.SpawnedParks(ctx)
		cancel()
	}
	roles, _, _ := f.Roles()
	names := make([]string, len(roles))
	for i, role := range roles {
		names[i] = role.Name
	}
	// /parks among the chat commands the server started with: the manager's,
	// the owner's or a plugin's in its place, or none yet.
	command := ""
	if c, ok := serverconfig.ChatCommands(in.Def().ConfigPath())[parks.Word]; ok {
		command = "taken"
		if c.Plugin() == parks.Source {
			command = "manager"
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"parks":   found,
		"setup":   setup,
		"limits":  map[string]int{"parks": limit, "objects": int(objects)},
		"spawned": spawned,
		"pools":   set.Pools,
		"chat":    set.Chat,
		"roles":   names,
		"command": command,
	})
}

// saveParks keeps the pools and who may use /parks.
func (a *API) saveParks(w http.ResponseWriter, r *http.Request) {
	in := inst(r)
	var req parks.Settings
	if !readJSON(w, r, &req) {
		return
	}
	if req.Pools == nil {
		req.Pools = []parks.Pool{}
	}
	if req.Chat.Roles == nil {
		req.Chat.Roles = []string{}
	}
	f, ok := readConfig(w, in)
	if !ok {
		return
	}
	setup, limit := parkSetup(f)
	if err := parks.Save(r.Context(), a.Store, in.ID(), req, setup, limit); err != nil {
		a.fail(w, r, 400, err)
		return
	}
	who := "nobody"
	switch {
	case req.Chat.Everyone:
		who = "everyone"
	case len(req.Chat.Roles) > 0:
		who = strings.Join(req.Chat.Roles, ", ")
	}
	a.audit(r, in.ID(), "parks.update", fmt.Sprintf("%d pools; /parks for %s", len(req.Pools), who))
	writeJSON(w, http.StatusOK, req)
}

// maxSpawn is the most parks the server spawns at once (park_limits.parks).
const maxSpawn = 16

// spawnParks puts parks up on the running server: all of them or none, for
// minutes when it's 1 to 1440, until removed when it's 0.
func (a *API) spawnParks(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Names   []string `json:"names"`
		Minutes int      `json:"minutes"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Minutes < 0 || req.Minutes > 1440 {
		writeErr(w, 400, "minutes are 1 to 1440, or 0 until removed")
		return
	}
	line, ok := parkLine(w, "park-mod add", req.Names)
	if !ok {
		return
	}
	if req.Minutes > 0 {
		line += " " + strconv.Itoa(req.Minutes)
	}
	a.parkCommand(w, r, "parks.spawn", line)
}

// removeParks takes parks down on the running server.
func (a *API) removeParks(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Names []string `json:"names"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if line, ok := parkLine(w, "park-mod remove", req.Names); ok {
		a.parkCommand(w, r, "parks.remove", line)
	}
}

// parkLine is verb with the park names, each checked, as nothing else may
// reach the console.
func parkLine(w http.ResponseWriter, verb string, names []string) (string, bool) {
	if len(names) == 0 || len(names) > maxSpawn {
		writeErr(w, 400, fmt.Sprintf("name 1 to %d parks", maxSpawn))
		return "", false
	}
	for i, n := range names {
		if !serverconfig.ValidParkName(n) || slices.Contains(names[:i], n) {
			writeErr(w, 400, fmt.Sprintf("%q isn't a park's name", n))
			return "", false
		}
	}
	return verb + " " + strings.Join(names, " "), true
}

// parkCommand runs a park-mod line. A park the server refuses is a 400 with
// its reason, so the page shows it as the failure it is.
func (a *API) parkCommand(w http.ResponseWriter, r *http.Request, action, line string) {
	in := inst(r)
	if in.State() != instance.Running {
		writeErr(w, http.StatusConflict, "parks spawn on a running server")
		return
	}
	reply, err := in.Command(r.Context(), line, from(r).user.Username)
	first, _, _ := strings.Cut(reply, "\n")
	if err == nil && strings.Contains(first, " could not be ") {
		err = errors.New(strings.TrimPrefix(first, "[parks] "))
	}
	a.auditOutcome(r, in.ID(), action, line, err)
	a.answerReply(w, r, reply, err)
}

// modFile is one of a park mod's files, ?path= in its folder, as text.
func (a *API) modFile(w http.ResponseWriter, r *http.Request) {
	folder, ok := modFolder(w, r)
	if !ok {
		return
	}
	text, hash, err := serverconfig.ReadModFile(inst(r).Def().Dir, folder, r.URL.Query().Get("path"))
	if err != nil {
		a.modErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": text, "hash": hash})
}

// saveModFile writes one of a park mod's files from its editor: the body is
// its text, ?path= which file, ?hash= what reading it gave. A file that
// changed since is a 409 with its hash now. The running server reads a park
// that's up again, in place.
func (a *API) saveModFile(w http.ResponseWriter, r *http.Request) {
	in := inst(r)
	folder, ok := modFolder(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	rel := q.Get("path")
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20+1))
	if _, tooBig := errors.AsType[*http.MaxBytesError](err); tooBig {
		writeErr(w, http.StatusRequestEntityTooLarge, "a park's file can be 2 MB at most")
		return
	} else if err != nil {
		writeErr(w, http.StatusBadRequest, "the upload was cut off")
		return
	}
	a.modMu.Lock()
	hash, err := serverconfig.WriteModFile(in.Def().Dir, folder, rel, string(body), q.Get("hash"))
	a.modMu.Unlock()
	if errors.Is(err, serverconfig.ErrModFileChanged) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": rel + " changed since it was opened", "hash": hash})
		return
	}
	if err != nil {
		a.modErr(w, r, err)
		return
	}
	a.audit(r, in.ID(), "mod.edit", folder+"/"+rel)
	writeJSON(w, http.StatusOK, map[string]string{"hash": hash, "reply": a.reloadParks(r, in, folder, rel)})
}

// reloadParks has the running server read the park at rel in folder again
// for each park mod that's up from it, and says what it did.
func (a *API) reloadParks(r *http.Request, in *instance.Instance, folder, rel string) string {
	key, ok := strings.CutPrefix(strings.TrimSuffix(rel, ".park.json"), "parks/")
	if !ok || in.State() != instance.Running {
		return ""
	}
	f, err := serverconfig.Read(in.Def().ConfigPath())
	if err != nil {
		return ""
	}
	setup, _ := parkSetup(f)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	spawned, err := in.SpawnedParks(ctx)
	if err != nil {
		return ""
	}
	var names []string
	for _, pm := range setup {
		// No key is the mod's only park.
		up := slices.ContainsFunc(spawned, func(s logparse.SpawnedPark) bool { return s.Name == pm.Name })
		if up && pm.Folder == folder && (pm.Key == "" || strings.EqualFold(pm.Key, key)) {
			names = append(names, pm.Name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	reply, err := in.Command(ctx, "park-mod reload "+strings.Join(names, " "), from(r).user.Username)
	if err != nil {
		return "Saved, but the server didn't read it again: " + err.Error()
	}
	return reply
}
