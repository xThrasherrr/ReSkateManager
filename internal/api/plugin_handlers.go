package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/xThrasherrr/ReSkateManager/internal/plugins"
	"github.com/xThrasherrr/ReSkateManager/internal/plugins/luavm"
)

// pluginsOn turns the plugins' routes away when the manager runs without them.
func (a *API) pluginsOn(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.Plugins == nil {
			writeErr(w, http.StatusNotFound, "plugins are off")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// pluginsInfo is the Plugins card: the plugins, and the rules they run by.
type pluginsInfo struct {
	Dir      string           `json:"dir"`
	API      int              `json:"api"`
	CallMs   int64            `json:"callMs"`
	MemoryMB int              `json:"memoryMB"`
	Events   []string         `json:"events"`
	Grants   []plugins.Grant  `json:"grants"`
	Plugins  []plugins.Status `json:"plugins"`
}

func (a *API) listPlugins(w http.ResponseWriter, r *http.Request) {
	list := a.Plugins.List()
	if list == nil {
		list = []plugins.Status{}
	}
	writeJSON(w, http.StatusOK, pluginsInfo{Dir: a.Plugins.Dir, API: plugins.API, CallMs: plugins.CallTime.Milliseconds(),
		MemoryMB: luavm.MemoryLimit >> 20, Events: plugins.Events, Grants: plugins.Grants, Plugins: list})
}

// setPlugin turns a plugin on or off, or changes what it's granted.
func (a *API) setPlugin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled *bool     `json:"enabled"`
		Granted *[]string `json:"granted"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Enabled == nil && req.Granted == nil {
		writeErr(w, http.StatusBadRequest, "say whether it's on, or what it's granted")
		return
	}
	id := chi.URLParam(r, "pid")
	if req.Granted != nil {
		if err := a.Plugins.SetGranted(r.Context(), id, *req.Granted); err != nil {
			a.pluginErr(w, r, err)
			return
		}
		what := "granted " + id + " nothing"
		if s, err := a.Plugins.Get(id); err == nil && len(s.Granted) > 0 {
			what = "granted " + id + " " + strings.Join(s.Granted, ", ")
		}
		a.audit(r, "", "manager.plugins", what)
	}
	if req.Enabled != nil {
		if err := a.Plugins.SetEnabled(r.Context(), id, *req.Enabled); err != nil {
			a.pluginErr(w, r, err)
			return
		}
		what := "turned off "
		if *req.Enabled {
			what = "turned on "
		}
		a.audit(r, "", "manager.plugins", what+id)
	}
	a.writePlugin(w, r, id)
}

func (a *API) reloadPlugin(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "pid")
	if err := a.Plugins.Reload(id); err != nil {
		a.pluginErr(w, r, err)
		return
	}
	a.audit(r, "", "manager.plugins", "reloaded "+id)
	a.writePlugin(w, r, id)
}

func (a *API) writePlugin(w http.ResponseWriter, r *http.Request, id string) {
	s, err := a.Plugins.Get(id)
	if err != nil {
		a.pluginErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s)
}

func (a *API) pluginErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, plugins.ErrNotFound):
		writeErr(w, http.StatusNotFound, "no such plugin")
		return
	case errors.Is(err, plugins.ErrBadGrant):
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	a.internalErr(w, r, err)
}
