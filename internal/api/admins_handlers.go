package api

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/xThrasherrr/ReSkateManager/internal/adminsync"
	"github.com/xThrasherrr/ReSkateManager/internal/auth"
	"github.com/xThrasherrr/ReSkateManager/internal/instance"
	"github.com/xThrasherrr/ReSkateManager/internal/serverconfig"
)

// liveAdminChange makes a running server's player an admin or not. From
// ReSkate 2.0.6 that's their role, and taking it away leaves them the default
// one: a player who isn't an admin keeps whatever role they have.
func (a *API) liveAdminChange(r *http.Request, in *instance.Instance, op, id string) (string, error) {
	f, err := serverconfig.Read(in.Def().ConfigPath())
	if err != nil {
		return "", err
	}
	if op == "add" {
		return in.Command(r.Context(), adminsync.AddCommand(f, id), from(r).user.Username)
	}
	if f.HasRoles() && !slices.Contains(f.Admins(), id) {
		return id + " is not an admin.", nil
	}
	return in.Command(r.Context(), adminsync.RemoveCommand(f, id), from(r).user.Username)
}

type adminRow struct {
	ID    string `json:"id"`
	Name  string `json:"name,omitempty"`
	Panel string `json:"panel,omitempty"` // the panel user this admin is kept for
}

// playerNames names the players a server has seen, by SteamID64: as they
// were last on, or are now.
func (a *API) playerNames(r *http.Request, in *instance.Instance) map[string]string {
	names := map[string]string{}
	if hist, err := a.Store.PlayerHistory(r.Context(), in.ID(), "", 5000); err == nil {
		for _, h := range hist {
			names[h.SteamID] = h.Name
		}
	}
	for _, p := range in.Players() {
		names[p.ID] = p.Name
	}
	return names
}

func (a *API) admins(w http.ResponseWriter, r *http.Request) {
	in := inst(r)
	f, ok := readConfig(w, in)
	if !ok {
		return
	}
	names := a.playerNames(r, in)
	panel, err := a.Admins.PanelAdmins(r.Context(), in.ID())
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	out := []adminRow{}
	for _, id := range f.Admins() {
		out = append(out, adminRow{ID: id, Name: names[id], Panel: panel[id]})
	}
	writeJSON(w, 200, out)
}

// roleRow is one of a server's roles and the players given it.
type roleRow struct {
	serverconfig.Role
	Players []adminRow `json:"players"`
}

// roles lists a server's roles (ReSkate 2.0.6) and who was given each, from
// its data/roles.json, which it rewrites whenever a role is given: so with
// nobody on, and while it's stopped.
func (a *API) roles(w http.ResponseWriter, r *http.Request) {
	in := inst(r)
	f, ok := readConfig(w, in)
	if !ok {
		return
	}
	list, given, def := f.Roles()
	names := a.playerNames(r, in)
	panel, err := a.Admins.PanelAdmins(r.Context(), in.ID())
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	ids := slices.Sorted(maps.Keys(given))
	out := []roleRow{}
	for _, role := range list {
		row := roleRow{Role: role, Players: []adminRow{}}
		for _, id := range ids {
			if given[id] == role.Name {
				row.Players = append(row.Players, adminRow{ID: id, Name: names[id], Panel: panel[id]})
			}
		}
		out = append(out, row)
	}
	writeJSON(w, 200, map[string]any{"roles": out, "default": def, "hasRoles": f.HasRoles()})
}

// errPanelAdmin is a role change the in-game admin sync would undo.
var errPanelAdmin = errors.New("kept in step with a panel user")

// panelUser names the panel user whose Steam account player id is, when the
// in-game admin sync makes them an admin on in; "" when it doesn't.
func (a *API) panelUser(r *http.Request, in *instance.Instance, id string) (string, error) {
	panel, err := a.Admins.PanelAdmins(r.Context(), in.ID())
	return panel[id], err
}

// giveRole gives a player one of a server's roles, or with role "" takes
// theirs away: with the role command while it runs, so they see it at once,
// else in its data/roles.json. An admin kept in step with a panel user is the
// sync's: it would make them an admin again.
func (a *API) giveRole(r *http.Request, in *instance.Instance, id, role string) (string, error) {
	user, err := a.panelUser(r, in, id)
	if err != nil {
		return "", err
	}
	if user != "" {
		return "", fmt.Errorf("%w: %s is an admin for panel user %s; change their roles or Steam link instead", errPanelAdmin, id, user)
	}
	if !running(in) {
		return in.EditStopped(func(f *serverconfig.File) (string, error) {
			if err := f.SetRole(id, role); err != nil {
				return "", err
			}
			// As the server says it.
			if _, _, def := f.Roles(); role == "" && def != "" {
				return id + " is " + def + " again.", nil
			} else if role == "" {
				return id + " no longer has a role.", nil
			}
			return id + " is now " + role + ".", nil
		})
	}
	f, err := serverconfig.Read(in.Def().ConfigPath())
	if err != nil {
		return "", err
	}
	list, given, _ := f.Roles()
	switch {
	case !f.HasRoles():
		return "", serverconfig.ErrNoRoles
	case role == "" && given[id] == "":
		return id + " was given no role.", nil
	case role != "" && !slices.ContainsFunc(list, func(x serverconfig.Role) bool { return x.Name == role }):
		return "", fmt.Errorf("the server has no %q role", role)
	}
	return in.Command(r.Context(), "role "+id+" "+cmp.Or(role, "none"), from(r).user.Username)
}

// answerRole answers a role change, or why there's none.
func (a *API) answerRole(w http.ResponseWriter, r *http.Request, reply string, err error) {
	if errors.Is(err, errPanelAdmin) || errors.Is(err, serverconfig.ErrNoRoles) {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	a.answerReply(w, r, reply, err)
}

// roleResult is how giving a role went on one server.
type roleResult struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Reply string `json:"reply,omitempty"`
	Error string `json:"error,omitempty"`
}

// setRole gives a player a role on this server, or on every server the user
// manages admins on: each defines its own roles, so one without the role is
// left out, and said so.
func (a *API) setRole(w http.ResponseWriter, r *http.Request) {
	in := inst(r)
	var req struct {
		ID         string `json:"id"`
		Role       string `json:"role"`
		Everywhere bool   `json:"everywhere"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	req.ID, req.Role = strings.TrimSpace(req.ID), strings.ToLower(strings.TrimSpace(req.Role))
	if !validSteamID(w, req.ID) {
		return
	}
	if req.Role == "" || req.Role == "none" {
		writeErr(w, 400, "pick a role")
		return
	}
	if !req.Everywhere {
		reply, err := a.giveRole(r, in, req.ID, req.Role)
		a.auditOutcome(r, in.ID(), "role.give", req.ID+" "+req.Role, err)
		a.answerRole(w, r, reply, err)
		return
	}
	results := []roleResult{}
	for _, other := range a.Reg.List() {
		if !from(r).perms.Can(auth.IngameAdmins, other.ID()) {
			continue
		}
		res := roleResult{ID: other.ID(), Name: other.Def().Name}
		reply, err := a.giveRole(r, other, req.ID, req.Role)
		a.auditOutcome(r, other.ID(), "role.give", req.ID+" "+req.Role, err)
		if err != nil {
			res.Error = err.Error()
		} else {
			res.Reply = reply
		}
		results = append(results, res)
	}
	writeJSON(w, 200, map[string]any{"results": results})
}

// takeRole takes a player's role away on this server.
func (a *API) takeRole(w http.ResponseWriter, r *http.Request) {
	in := inst(r)
	id, ok := steamParam(w, r)
	if !ok {
		return
	}
	reply, err := a.giveRole(r, in, id, "")
	a.auditOutcome(r, in.ID(), "role.take", id, err)
	a.answerRole(w, r, reply, err)
}

// roleChange makes change to a server's roles in its data/roles.json, as the
// server's own roles editor does (Instance.ChangeRoles).
func (a *API) roleChange(w http.ResponseWriter, r *http.Request, action, detail string, change func(*serverconfig.File) (string, error)) {
	in := inst(r)
	reply, err := in.ChangeRoles(r.Context(), from(r).user.Username, change)
	a.auditOutcome(r, in.ID(), action, detail, err)
	a.answerRole(w, r, reply, err)
}

// addServerRole adds a role to a server (ReSkate 2.0.6), with the tag, colour and
// rank given and what the default role has.
func (a *API) addServerRole(w http.ResponseWriter, r *http.Request) {
	var req serverconfig.RoleLook
	if !readJSON(w, r, &req) {
		return
	}
	req.Name = strings.ToLower(strings.TrimSpace(req.Name))
	a.roleChange(w, r, "role.create", req.Name, func(f *serverconfig.File) (string, error) {
		return "Added the " + req.Name + " role.", f.AddRole(req)
	})
}

// editServerRole sets a role's tag, colour and rank.
func (a *API) editServerRole(w http.ResponseWriter, r *http.Request) {
	name, _ := pathParam(r, "role")
	var req serverconfig.RoleLook
	if !readJSON(w, r, &req) {
		return
	}
	a.roleChange(w, r, "role.edit", name, func(f *serverconfig.File) (string, error) {
		return "Saved the " + name + " role.", f.EditRole(name, req)
	})
}

// deleteServerRole deletes a role; its players are the default role again.
func (a *API) deleteServerRole(w http.ResponseWriter, r *http.Request) {
	name, _ := pathParam(r, "role")
	a.roleChange(w, r, "role.delete", name, func(f *serverconfig.File) (string, error) {
		n, err := f.DeleteRole(name)
		if err != nil || n == 0 {
			return "Deleted the " + name + " role.", err
		}
		who := "the player who had it is"
		if n > 1 {
			who = fmt.Sprintf("the %d players who had it are", n)
		}
		if _, _, def := f.Roles(); def != "" {
			return fmt.Sprintf("Deleted the %s role; %s %s again.", name, who, def), nil
		}
		return fmt.Sprintf("Deleted the %s role; %s given no role now.", name, who), nil
	})
}

func (a *API) addAdmin(w http.ResponseWriter, r *http.Request) {
	in := inst(r)
	var req struct {
		ID string `json:"id"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if !validSteamID(w, req.ID) {
		return
	}
	a.adminChange(w, r, in, "add", req.ID)
}

func (a *API) removeAdmin(w http.ResponseWriter, r *http.Request) {
	in := inst(r)
	id, ok := steamParam(w, r)
	if !ok {
		return
	}
	user, err := a.panelUser(r, in, id)
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	if user != "" {
		writeErr(w, 409, "this admin comes from panel user "+user+"; change their roles or Steam link instead")
		return
	}
	a.adminChange(w, r, in, "remove", id)
}

func (a *API) adminChange(w http.ResponseWriter, r *http.Request, in *instance.Instance, op, id string) {
	var reply string
	var err error
	if running(in) {
		reply, err = a.liveAdminChange(r, in, op, id)
	} else {
		reply, err = in.EditStopped(func(f *serverconfig.File) (string, error) {
			list := f.Admins()
			if op == "add" {
				if !slices.Contains(list, id) {
					list = append(list, id)
				}
				return id + " is an admin.", f.SetAdmins(list)
			}
			return id + " is no longer an admin.", f.SetAdmins(slices.DeleteFunc(list, func(x string) bool { return x == id }))
		})
	}
	a.auditOutcome(r, in.ID(), "admin."+op, id, err)
	a.answerReply(w, r, reply, err)
}
