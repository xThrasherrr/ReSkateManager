package api

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/xThrasherrr/ReSkateManager/internal/instance"
	"github.com/xThrasherrr/ReSkateManager/internal/serverconfig"
	"github.com/xThrasherrr/ReSkateManager/internal/store"
)

// The plugins players must have to join (plugins.required, ReSkate 2.1.2)
// are the manager's to keep: for every server, and for each server besides.
// It puts them in a server's config as the server starts, the only time the
// server reads them, and takes out the ones it put there that neither list
// has any more. What else the config requires stays.

// requiredKey keeps every server's list; requiredKey+":"+id one server's
// (store.DeleteInstance deletes it with the server).
const requiredKey = "required-plugins"

// serverRequired is one server's list, and what the manager put in its
// config at its last start.
type serverRequired struct {
	Plugins []string `json:"plugins"`
	Added   []string `json:"added"`
}

func (a *API) everyRequired(ctx context.Context) ([]string, error) {
	return store.LoadJSON(ctx, a.Store, requiredKey, []string{})
}

func (a *API) serverRequired(ctx context.Context, id string) (serverRequired, error) {
	return store.LoadJSON(ctx, a.Store, requiredKey+":"+id, serverRequired{Plugins: []string{}, Added: []string{}})
}

// requiredPlan is a server's lists, and its config's required plugins now
// and as the manager leaves them for its next start. Without any list, the
// config isn't read (f is nil).
type requiredPlan struct {
	own                  serverRequired
	every                []string
	f                    *serverconfig.File
	now, next, put, left []string
}

func (a *API) planRequired(ctx context.Context, def instance.Def) (*requiredPlan, error) {
	p := &requiredPlan{}
	var err error
	if p.every, err = a.everyRequired(ctx); err != nil {
		return nil, err
	}
	if p.own, err = a.serverRequired(ctx, def.ID); err != nil {
		return nil, err
	}
	if len(p.every)+len(p.own.Plugins)+len(p.own.Added) == 0 {
		return p, nil
	}
	if p.f, err = serverconfig.Read(def.ConfigPath()); err != nil {
		return nil, err
	}
	p.now, _ = p.f.Values()["plugins.required"].([]string)
	p.next, p.put, p.left = serverconfig.MergeRequired(p.now, p.own.Added, append(slices.Clone(p.own.Plugins), p.every...))
	return p, nil
}

// RequirePlugins is the hook for a server's RequirePlugins: it puts both
// lists in the config, and says what didn't fit. A server before 2.1.2 has
// no plugins to require.
func (a *API) RequirePlugins(def instance.Def) (string, error) {
	if a.Store == nil || serverconfig.NewerVersion("2.1.2", a.Builds.Version(def.Exe())) {
		return "", nil
	}
	ctx := context.Background()
	p, err := a.planRequired(ctx, def)
	if err != nil || p.f == nil {
		return "", err
	}
	if !slices.Equal(p.next, p.now) {
		p.f.ApplyOffline(map[string]any{"plugins.required": p.next})
		if err := p.f.Write(def.ConfigPath()); err != nil {
			return "", err
		}
	}
	p.own.Added = p.put
	if err := store.SaveJSON(ctx, a.Store, requiredKey+":"+def.ID, p.own); err != nil {
		return "", err
	}
	if len(p.left) > 0 {
		return "Players aren't asked for " + strings.Join(p.left, ", ") + ": the server takes at most 16 required plugins.", nil
	}
	return "", nil
}

func (a *API) requiredPlugins(w http.ResponseWriter, r *http.Request) {
	every, err := a.everyRequired(r.Context())
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"plugins": every})
}

// saveRequired keeps the plugins every server requires. Each server takes
// them as it next starts.
func (a *API) saveRequired(w http.ResponseWriter, r *http.Request) {
	list, ok := readRequired(w, r)
	if !ok {
		return
	}
	if err := store.SaveJSON(r.Context(), a.Store, requiredKey, list); err != nil {
		a.internalErr(w, r, err)
		return
	}
	a.audit(r, "", "required.every", strings.Join(list, ", "))
	writeJSON(w, 200, map[string]any{"plugins": list})
}

// saveServerRequired keeps the plugins one server requires besides every
// server's. The server takes them as it next starts.
func (a *API) saveServerRequired(w http.ResponseWriter, r *http.Request) {
	in := inst(r)
	list, ok := readRequired(w, r)
	if !ok {
		return
	}
	own, err := a.serverRequired(r.Context(), in.ID())
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	own.Plugins = list
	if err := store.SaveJSON(r.Context(), a.Store, requiredKey+":"+in.ID(), own); err != nil {
		a.internalErr(w, r, err)
		return
	}
	a.audit(r, in.ID(), "required.server", strings.Join(list, ", "))
	writeJSON(w, 200, map[string]any{"plugins": list})
}

func readRequired(w http.ResponseWriter, r *http.Request) ([]string, bool) {
	var req struct {
		Plugins []string `json:"plugins"`
	}
	if !readJSON(w, r, &req) {
		return nil, false
	}
	list, err := serverconfig.CleanRequired(req.Plugins)
	if err != nil {
		writeErr(w, 400, err.Error())
		return nil, false
	}
	return list, true
}
