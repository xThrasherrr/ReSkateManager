package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/instance"
	"github.com/xThrasherrr/ReSkateManager/internal/modprofile"
	"github.com/xThrasherrr/ReSkateManager/internal/serverconfig"
	"github.com/xThrasherrr/ReSkateManager/internal/thunderstore"
)

// Launcher profiles (ReSkate 2.0.8): the launcher, like r2modman, loads a
// list of mods at the versions it names and downloads what's missing from
// Thunderstore. A server shares the maps it loads as one, so players get them
// all at once, and takes the mods of one that a player or its owner made.

// profileMod is a mod of the profile a server shares.
type profileMod struct {
	Package string `json:"package"` // Namespace-Name, as Thunderstore spells it
	Version string `json:"version"`
	Title   string `json:"title"`
	// The server's version, when Thunderstore no longer has it and the
	// profile gives the newest instead.
	Server string `json:"server,omitempty"`
}

// leftOut is a mod the server loads maps from that the profile can't give.
type leftOut struct {
	Title string `json:"title"`
	Why   string `json:"why"`
}

type sharedProfile struct {
	Name    string       `json:"name"`
	Mods    []profileMod `json:"mods"`
	Left    []leftOut    `json:"left"`
	profile modprofile.Profile
}

// shareCode is the code a server's profile was last shared under.
type shareCode struct {
	text, code string // the profile's export.r2x, and its code
	at         time.Time
}

// shareReuse is how long a code is given again for the same profile, rather
// than a new one each time someone asks.
const shareReuse = time.Hour

// maxProfileFile caps a .r2z sent to read; r2modman's carry config files.
const maxProfileFile = 32 << 20

// playerProfile is the profile a server shares: every enabled mod it loads a
// map from that Thunderstore has, at the server's version, in the order the
// server reads them (where two add the same map, the first wins in the game
// too). On failure it has answered the request.
func (a *API) playerProfile(w http.ResponseWriter, r *http.Request, in *instance.Instance) (*sharedProfile, bool) {
	pkgs, ok := a.packages(w, r, false)
	if !ok {
		return nil, false
	}
	mods, err := serverconfig.ReadMods(in.Def().Dir)
	if err != nil {
		a.internalErr(w, r, err)
		return nil, false
	}
	sp := &sharedProfile{Name: modprofile.CleanName(gameName(in)), Mods: []profileMod{}, Left: []leftOut{}}
	seen := map[string]bool{}
	for _, m := range mods {
		if m.Disabled || m.Problem != "" || !slices.ContainsFunc(m.Maps, func(x serverconfig.ModMap) bool { return !x.Shadowed }) {
			continue // the server loads no map from it
		}
		p := pkgs[strings.ToLower(m.Package)]
		if m.Package == "" || p == nil || p.Latest() == nil {
			sp.Left = append(sp.Left, leftOut{m.Title, "not on Thunderstore"})
			continue
		}
		if seen[strings.ToLower(p.FullName)] {
			continue
		}
		seen[strings.ToLower(p.FullName)] = true
		pm := profileMod{Package: p.FullName, Version: m.Version, Title: strings.ReplaceAll(p.Name, "_", " ")}
		if p.Version(m.Version) == nil {
			pm.Version, pm.Server = p.Latest().Number, m.Version
		}
		sp.Mods = append(sp.Mods, pm)
		// The launcher keeps a package in a folder of its full name, cut to fit.
		sp.profile.Mods = append(sp.profile.Mods, modprofile.Mod{Name: p.FullName[:min(len(p.FullName), modprofile.MaxModName)], Version: pm.Version, Enabled: true})
	}
	sp.profile.Name = sp.Name
	return sp, true
}

// gameName is the name players see the server by, or its name in the panel
// before it has a config.
func gameName(in *instance.Instance) string {
	if f, err := serverconfig.Read(in.Def().ConfigPath()); err == nil {
		raw, _ := f.Get("name")
		if name, _ := raw.(string); strings.TrimSpace(name) != "" {
			return name
		}
	}
	return in.Def().Name
}

// modProfile answers what the profile a server shares holds.
func (a *API) modProfile(w http.ResponseWriter, r *http.Request) {
	if sp, ok := a.playerProfile(w, r, inst(r)); ok {
		writeJSON(w, 200, sp)
	}
}

// modProfileFile is the shared profile as a .r2z, which the launcher and
// r2modman import.
func (a *API) modProfileFile(w http.ResponseWriter, r *http.Request) {
	sp, ok := a.playerProfile(w, r, inst(r))
	if !ok {
		return
	}
	b, err := sp.profile.Archive()
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	attachment(w, sp.Name+".r2z")
	w.Write(b)
}

// modProfileCode puts the shared profile on Thunderstore's profile service
// and answers its code. The same profile gets the same code for a while.
func (a *API) modProfileCode(w http.ResponseWriter, r *http.Request) {
	in := inst(r)
	sp, ok := a.playerProfile(w, r, in)
	if !ok {
		return
	}
	if len(sp.Mods) == 0 {
		writeErr(w, http.StatusConflict, "the server loads no maps from mods on Thunderstore, so there is nothing to share")
		return
	}
	text := sp.profile.Export()
	a.shareMu.Lock()
	last, had := a.shareCodes[in.ID()]
	a.shareMu.Unlock()
	if had && last.text == text && time.Since(last.at) < shareReuse {
		writeJSON(w, 200, map[string]any{"code": last.code})
		return
	}
	payload, err := sp.profile.Payload()
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	code, err := a.Thunderstore.ShareProfile(r.Context(), payload)
	if err != nil {
		a.fail(w, r, http.StatusBadGateway, fmt.Errorf("could not get a code from Thunderstore: %w", err))
		return
	}
	a.shareMu.Lock()
	if a.shareCodes == nil {
		a.shareCodes = map[string]shareCode{}
	}
	a.shareCodes[in.ID()] = shareCode{text, code, time.Now()}
	a.shareMu.Unlock()
	a.audit(r, in.ID(), "mods.share", code)
	writeJSON(w, 200, map[string]any{"code": code})
}

// profileEntry is one mod of a profile read to install from, as it stands
// on this server.
type profileEntry struct {
	Name    string `json:"name"` // as the profile has them
	Version string `json:"version"`
	Enabled bool   `json:"enabled"`
	// Thunderstore's package, and the version to install: the profile's, or
	// the newest where Thunderstore no longer has that one. Empty when
	// Thunderstore hasn't the package.
	Package string `json:"package,omitempty"`
	Title   string `json:"title,omitempty"`
	Install string `json:"install,omitempty"`
	Size    int64  `json:"size,omitempty"`
	// new | replaces (another version, Replaces) | installed (Note says
	// how) | absent (not on Thunderstore).
	State    string `json:"state"`
	Replaces string `json:"replaces,omitempty"`
	Note     string `json:"note,omitempty"`
	// The maps the version adds once someone has looked, null until then,
	// and why it can't be looked inside.
	Maps    []serverconfig.ModMap `json:"maps"`
	Unknown string                `json:"unknown,omitempty"`
}

// readModProfile reads a profile to install mods from: a code, as JSON
// {"code"}, or the .r2z (or a bare export.r2x) as the body. Installing goes
// through the server's installs, one mod at a time.
func (a *API) readModProfile(w http.ResponseWriter, r *http.Request) {
	in := inst(r)
	var prof modprofile.Profile
	var err error
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var req struct {
			Code string `json:"code"`
		}
		if !readJSON(w, r, &req) {
			return
		}
		code := strings.TrimSpace(req.Code)
		switch {
		case a.Thunderstore == nil:
			writeErr(w, 404, "Thunderstore is turned off")
			return
		case !modprofile.ValidCode(code):
			writeErr(w, 400, "a profile code is 8 to 64 letters a to f, digits and dashes")
			return
		}
		payload, err := a.Thunderstore.FetchProfile(r.Context(), code)
		if err != nil {
			a.fail(w, r, http.StatusBadGateway, err)
			return
		}
		if prof, err = modprofile.ParsePayload(payload); err != nil {
			a.fail(w, r, 400, err)
			return
		}
	} else {
		b, rerr := io.ReadAll(http.MaxBytesReader(w, r.Body, maxProfileFile))
		if _, ok := errors.AsType[*http.MaxBytesError](rerr); ok {
			writeErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("a profile file can be at most %d MB", maxProfileFile>>20))
			return
		}
		if rerr != nil {
			a.fail(w, r, 400, rerr)
			return
		}
		if prof, err = modprofile.ParseArchive(b); err != nil {
			a.fail(w, r, 400, err)
			return
		}
	}
	pkgs, ok := a.packages(w, r, false)
	if !ok {
		return
	}
	mods, err := serverconfig.ReadMods(in.Def().Dir)
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	out := make([]profileEntry, 0, len(prof.Mods))
	for _, m := range prof.Mods {
		out = append(out, a.profileEntry(m, pkgs, mods))
	}
	writeJSON(w, 200, map[string]any{"name": prof.Name, "mods": out})
}

// profileEntry is how a profile's mod stands against Thunderstore's packages
// and the server's mods, as an install of it would find it.
func (a *API) profileEntry(m modprofile.Mod, pkgs map[string]*thunderstore.Package, mods []serverconfig.Mod) profileEntry {
	e := profileEntry{Name: m.Name, Version: m.Version, Enabled: m.Enabled, State: "absent"}
	p := pkgs[strings.ToLower(m.Name)]
	if p == nil || p.Latest() == nil {
		return e
	}
	v := p.Version(m.Version)
	if v == nil {
		v = p.Latest()
	}
	e.Package, e.Title, e.Install, e.Size = p.FullName, strings.ReplaceAll(p.Name, "_", " "), v.Number, v.FileSize
	if pr := a.knownMaps(v); pr != nil {
		e.Maps, e.Unknown = pr.maps, pr.unknown
	}
	replace, err := installedAs(mods, p, v, "This server")
	switch {
	case err != nil:
		e.State, e.Note = "installed", err.Error()
	case replace != "":
		e.State = "replaces"
		for _, have := range mods {
			if have.Folder == replace {
				e.Replaces = have.Version
			}
		}
	default:
		e.State = "new"
	}
	return e
}
