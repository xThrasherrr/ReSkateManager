package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/xThrasherrr/ReSkateManager/internal/auth"
)

func (a *API) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := a.Auth.Users(r.Context())
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	if users == nil {
		users = []*auth.User{}
	}
	writeJSON(w, 200, users)
}

// Only owners may create or edit owners. Everyone else may only hand out
// permissions they hold, and only edit users who hold no more than they do, so
// panel.users.manage cannot be used to climb.
func (a *API) createUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string       `json:"username"`
		Password string       `json:"password"`
		SteamID  string       `json:"steamId"`
		Owner    bool         `json:"owner"`
		Grants   []auth.Grant `json:"grants"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	c := from(r)
	if req.Owner && !c.perms.Owner {
		writeErr(w, 403, "only owners can create owners")
		return
	}
	if err := a.checkGrants(req.Grants); err != nil {
		a.fail(w, r, 400, err)
		return
	}
	if !a.mayGrant(w, r, req.Grants) {
		return
	}
	u, err := a.Auth.CreateUser(r.Context(), auth.NewUser{Username: strings.TrimSpace(req.Username), Password: req.Password,
		SteamID: strings.TrimSpace(req.SteamID), Owner: req.Owner, Grants: req.Grants})
	if err != nil {
		a.fail(w, r, 400, err)
		return
	}
	a.audit(r, "", "user.create", fmt.Sprintf("%s: owner=%t, roles %s", u.Username, u.Owner, grantsText(u.Grants)))
	a.Admins.Kick()
	writeJSON(w, 201, u)
}

func (a *API) checkGrants(grants []auth.Grant) error {
	for _, g := range grants {
		if g.Instance == "" || g.Instance == "*" {
			continue
		}
		if _, ok := a.Reg.Get(g.Instance); !ok {
			return fmt.Errorf("no server %q", g.Instance)
		}
	}
	return nil
}

// mayGrant refuses grants that carry permissions the editor lacks.
func (a *API) mayGrant(w http.ResponseWriter, r *http.Request, grants []auth.Grant) bool {
	p, err := a.Auth.GrantPerms(r.Context(), grants)
	if err != nil {
		a.fail(w, r, 400, err)
		return false
	}
	if !from(r).perms.Covers(p) {
		writeErr(w, 403, "you can only grant permissions you hold yourself")
		return false
	}
	return true
}

// mayEdit refuses changes to a user who holds permissions the editor lacks:
// resetting their password or Steam link would hand those permissions over.
func (a *API) mayEdit(w http.ResponseWriter, r *http.Request, target *auth.User) bool {
	p, err := a.Auth.Perms(r.Context(), target)
	if err != nil {
		a.internalErr(w, r, err)
		return false
	}
	if !from(r).perms.Covers(p) {
		writeErr(w, 403, "this user holds permissions you don't")
		return false
	}
	return true
}

// mayEditRole refuses role edits that touch permissions the editor lacks.
func (a *API) mayEditRole(w http.ResponseWriter, r *http.Request, perms []string) bool {
	if !from(r).perms.HoldsAll(perms) {
		writeErr(w, 403, "you can only edit roles whose permissions you hold on all servers")
		return false
	}
	return true
}

// mayChangeHolders refuses changing or removing a role held by someone who
// holds permissions the editor lacks: as with editing that user, what they
// may do isn't the editor's call. Owners don't count, as no role changes
// what they may do.
func (a *API) mayChangeHolders(w http.ResponseWriter, r *http.Request, roleID int64) bool {
	holders, err := a.Auth.RoleHolders(r.Context(), roleID)
	if err != nil {
		a.internalErr(w, r, err)
		return false
	}
	for _, u := range holders {
		if u.Owner {
			continue
		}
		p, err := a.Auth.Perms(r.Context(), u)
		if err != nil {
			a.internalErr(w, r, err)
			return false
		}
		if !from(r).perms.Covers(p) {
			writeErr(w, 403, u.Username+" holds this role and permissions you don't, so only someone who holds those can change it")
			return false
		}
	}
	return true
}

// grantsText names grants for the audit log, as "Moderator on lobby, Viewer
// on all servers".
func grantsText(grants []auth.Grant) string {
	if len(grants) == 0 {
		return "none"
	}
	parts := make([]string, len(grants))
	for i, g := range grants {
		on := g.Instance
		if on == "" || on == "*" {
			on = "all servers"
		}
		parts[i] = g.Role + " on " + on
	}
	return strings.Join(parts, ", ")
}

func userParam(w http.ResponseWriter, r *http.Request) (int64, bool) {
	return int64Param(w, r, "uid", "user")
}

func (a *API) updateUser(w http.ResponseWriter, r *http.Request) {
	id, ok := userParam(w, r)
	if !ok {
		return
	}
	var up auth.UserUpdate
	if !readJSON(w, r, &up) {
		return
	}
	c := from(r)
	target, err := a.Auth.User(r.Context(), id)
	if err != nil {
		a.fail(w, r, errStatus(err), err)
		return
	}
	if (target.Owner || up.Owner != nil) && !c.perms.Owner {
		writeErr(w, 403, "only owners can change owners")
		return
	}
	if !a.mayEdit(w, r, target) {
		return
	}
	if up.Grants != nil {
		if err := a.checkGrants(*up.Grants); err != nil {
			a.fail(w, r, 400, err)
			return
		}
		if !a.mayGrant(w, r, *up.Grants) {
			return
		}
	}
	u, err := a.Auth.UpdateUser(r.Context(), id, up)
	if err != nil {
		a.fail(w, r, errStatus(err), err)
		return
	}
	// A new password or another Steam account ends the user's sessions; one
	// changing their own keeps going.
	steamChanged := up.SteamID != nil && target.SteamID != "" && *up.SteamID != target.SteamID
	if id == c.user.ID && (up.Password != nil || steamChanged) {
		if err := a.startSession(w, r, u); err != nil {
			a.internalErr(w, r, err)
			return
		}
	}
	var what []string
	if up.Username != nil && target.Username != u.Username {
		what = append(what, "renamed from "+target.Username)
	}
	if up.Password != nil {
		what = append(what, "new password")
	}
	if up.SteamID != nil && *up.SteamID != target.SteamID {
		if *up.SteamID == "" {
			what = append(what, "Steam unlinked")
		} else {
			what = append(what, "Steam "+*up.SteamID)
		}
	}
	if up.Owner != nil && *up.Owner != target.Owner {
		what = append(what, fmt.Sprintf("owner=%t", *up.Owner))
	}
	if up.Disabled != nil && *up.Disabled != target.Disabled {
		what = append(what, fmt.Sprintf("disabled=%t", *up.Disabled))
	}
	if up.Grants != nil {
		what = append(what, "roles "+grantsText(u.Grants))
	}
	if len(what) == 0 {
		what = append(what, "no change")
	}
	a.audit(r, "", "user.update", u.Username+": "+strings.Join(what, ", "))
	a.Admins.Kick()
	writeJSON(w, 200, u)
}

func (a *API) deleteUser(w http.ResponseWriter, r *http.Request) {
	id, ok := userParam(w, r)
	if !ok {
		return
	}
	c := from(r)
	if id == c.user.ID {
		writeErr(w, 400, "you cannot remove yourself")
		return
	}
	target, err := a.Auth.User(r.Context(), id)
	if err != nil {
		a.fail(w, r, errStatus(err), err)
		return
	}
	if target.Owner && !c.perms.Owner {
		writeErr(w, 403, "only owners can remove owners")
		return
	}
	if !a.mayEdit(w, r, target) {
		return
	}
	if err := a.Auth.DeleteUser(r.Context(), id); err != nil {
		a.fail(w, r, errStatus(err), err)
		return
	}
	a.audit(r, "", "user.delete", target.Username)
	a.Admins.Kick()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *API) listRoles(w http.ResponseWriter, r *http.Request) {
	roles, err := a.Auth.Roles(r.Context())
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	writeJSON(w, 200, roles)
}

func (a *API) saveRole(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string   `json:"name"`
		Permissions []string `json:"permissions"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	role := auth.Role{Name: req.Name, Permissions: req.Permissions}
	if chi.URLParam(r, "rid") != "" {
		id, ok := int64Param(w, r, "rid", "role")
		if !ok {
			return
		}
		role.ID = id
		old, err := a.Auth.Role(r.Context(), id)
		if err != nil {
			a.fail(w, r, errStatus(err), err)
			return
		}
		if !a.mayEditRole(w, r, old.Permissions) || !a.mayChangeHolders(w, r, id) {
			return
		}
	} else {
		role.ID = 0
	}
	if !a.mayEditRole(w, r, role.Permissions) {
		return
	}
	saved, err := a.Auth.SaveRole(r.Context(), role)
	if err != nil {
		a.fail(w, r, errStatus(err), err)
		return
	}
	a.audit(r, "", "role.save", saved.Name+": "+strings.Join(saved.Permissions, ", "))
	a.Admins.Kick()
	writeJSON(w, 200, saved)
}

func (a *API) deleteRole(w http.ResponseWriter, r *http.Request) {
	id, ok := int64Param(w, r, "rid", "role")
	if !ok {
		return
	}
	old, err := a.Auth.Role(r.Context(), id)
	if err != nil {
		a.fail(w, r, errStatus(err), err)
		return
	}
	if !a.mayEditRole(w, r, old.Permissions) || !a.mayChangeHolders(w, r, id) {
		return
	}
	if err := a.Auth.DeleteRole(r.Context(), id); err != nil {
		a.internalErr(w, r, err)
		return
	}
	a.audit(r, "", "role.delete", old.Name)
	a.Admins.Kick()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *API) auditLog(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	rows, err := a.Store.AuditLog(r.Context(), q.Get("instance"), before, 100)
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	writeJSON(w, 200, rows)
}
