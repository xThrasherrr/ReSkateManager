package api

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/xThrasherrr/ReSkateManager/internal/plugins"
	"github.com/xThrasherrr/ReSkateManager/internal/plugins/luavm"
	"github.com/xThrasherrr/ReSkateManager/internal/store"
)

// boards is playtime and the boards plugins marked public, for a website:
// ?server= (every server), ?period=all|month|week (all) and ?limit= (10, up
// to 100).
func (a *API) boards(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	server := q.Get("server")
	if _, ok := a.Reg.Get(server); server != "" && !ok {
		writeErr(w, http.StatusNotFound, "no such server")
		return
	}
	period := cmp.Or(q.Get("period"), "all")
	if !slices.Contains(store.Periods, period) {
		writeErr(w, http.StatusBadRequest, `period is "all", "month" or "week"`)
		return
	}
	limit := 10
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 100 {
			writeErr(w, http.StatusBadRequest, "limit is 1 to 100")
			return
		}
		limit = n
	}
	list := a.Plugins.PublicBoards(server, period, limit)
	if list == nil {
		list = []plugins.Top{}
	}
	writeJSON(w, http.StatusOK, list)
}

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
	MaxMB    int              `json:"maxUploadMB"`
	Plugins  []plugins.Status `json:"plugins"`
}

func (a *API) listPlugins(w http.ResponseWriter, r *http.Request) {
	list := a.Plugins.List()
	if list == nil {
		list = []plugins.Status{}
	}
	writeJSON(w, http.StatusOK, pluginsInfo{Dir: a.Plugins.Dir, API: plugins.API, CallMs: plugins.CallTime.Milliseconds(),
		MemoryMB: luavm.MemoryLimit >> 20, Events: plugins.Events, Grants: plugins.Grants, MaxMB: plugins.MaxUpload >> 20, Plugins: list})
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

// addPlugin installs a plugin from a zip of its folder, the request's body.
// ?name= is the zip's file name, which names a plugin zipped without a
// folder; ?replace=1 replaces one that's there already.
func (a *API) addPlugin(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, plugins.MaxUpload))
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		writeErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("a plugin's zip can be %d MB at most", plugins.MaxUpload>>20))
		return
	case err != nil:
		writeErr(w, http.StatusBadRequest, "the upload was cut off")
		return
	}
	st, replaced, err := a.Plugins.Install(data, r.URL.Query().Get("name"), r.URL.Query().Get("replace") == "1")
	var exists *plugins.ExistsError
	switch {
	case errors.As(err, &exists):
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "plugin": exists.Plugin})
		return
	case errors.Is(err, plugins.ErrBadUpload):
		writeErr(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), plugins.ErrBadUpload.Error()+": "))
		return
	case err != nil:
		a.internalErr(w, r, err)
		return
	}
	detail, code := "added "+st.ID, http.StatusCreated
	if replaced {
		detail, code = "replaced "+st.ID, http.StatusOK
	}
	if st.Version != "" {
		detail += " " + st.Version
	}
	a.audit(r, "", "manager.plugins", detail)
	writeJSON(w, code, st)
}

// removePlugin stops a plugin and deletes its folder.
func (a *API) removePlugin(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "pid")
	if err := a.Plugins.Remove(r.Context(), id); err != nil {
		a.pluginErr(w, r, err)
		return
	}
	a.audit(r, "", "manager.plugins", "removed "+id)
	w.WriteHeader(http.StatusNoContent)
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

// pluginFiles lists a plugin's files for its editor.
func (a *API) pluginFiles(w http.ResponseWriter, r *http.Request) {
	files, err := a.Plugins.Files(chi.URLParam(r, "pid"))
	if err != nil {
		a.pluginErr(w, r, err)
		return
	}
	if files == nil {
		files = []plugins.File{}
	}
	writeJSON(w, http.StatusOK, files)
}

// pluginFile is one of a plugin's files, ?path= in its folder, as text.
func (a *API) pluginFile(w http.ResponseWriter, r *http.Request) {
	text, hash, err := a.Plugins.ReadFile(chi.URLParam(r, "pid"), r.URL.Query().Get("path"))
	if err != nil {
		a.pluginErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": text, "hash": hash})
}

// savePluginFile writes one of a plugin's files from its editor: the body
// is its text, ?path= where it goes, and ?hash= what reading it gave, none
// for a new file. A file that changed since is a 409 with its hash now.
func (a *API) savePluginFile(w http.ResponseWriter, r *http.Request) {
	id, q := chi.URLParam(r, "pid"), r.URL.Query()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, plugins.MaxUpload))
	if _, tooBig := errors.AsType[*http.MaxBytesError](err); tooBig {
		writeErr(w, http.StatusRequestEntityTooLarge, "a plugin's file can be 1 MB at most")
		return
	} else if err != nil {
		writeErr(w, http.StatusBadRequest, "the upload was cut off")
		return
	}
	hash, err := a.Plugins.WriteFile(id, q.Get("path"), string(body), q.Get("hash"))
	if errors.Is(err, plugins.ErrChanged) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": q.Get("path") + " changed since it was opened", "hash": hash})
		return
	}
	if err != nil {
		a.pluginErr(w, r, err)
		return
	}
	a.audit(r, "", "manager.plugins", "edited "+id+"/"+q.Get("path"))
	writeJSON(w, http.StatusOK, map[string]string{"hash": hash})
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
	case errors.Is(err, plugins.ErrBadFile):
		writeErr(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), plugins.ErrBadFile.Error()+": "))
		return
	}
	a.internalErr(w, r, err)
}
