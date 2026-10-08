package api

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/auth"
	"github.com/xThrasherrr/ReSkateManager/internal/backup"
	"github.com/xThrasherrr/ReSkateManager/internal/serverconfig"
)

func (a *API) setCookie(w http.ResponseWriter, r *http.Request, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   a.secure(r),
		// Lax, not Strict: the Steam callback is a cross-site navigation that must carry the session when linking.
		SameSite: http.SameSiteLaxMode,
	})
}

func (a *API) startSession(w http.ResponseWriter, r *http.Request, u *auth.User) error {
	token, err := a.Auth.NewSession(r.Context(), u.ID, a.clientIP(r), r.UserAgent())
	if err != nil {
		return err
	}
	a.setCookie(w, r, token, int(auth.SessionTTL.Seconds()))
	return nil
}

func (a *API) me(w http.ResponseWriter, r *http.Request) {
	setup, err := a.Auth.SetupRequired(r.Context())
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	_, steam := a.steamBase(r)
	out := map[string]any{"setupRequired": setup, "steam": steam}
	// The version only to users: to anyone else it says which flaws to try.
	if u, _ := a.sessionUser(r); u != nil {
		out["version"] = a.Version
		perms, err := a.Auth.Perms(r.Context(), u)
		if err != nil {
			a.internalErr(w, r, err)
			return
		}
		out["user"], out["perms"] = u, perms
	}
	writeJSON(w, 200, out)
}

func (a *API) meta(w http.ResponseWriter, r *http.Request) {
	fields := serverconfig.Fields
	if !from(r).perms.Owner {
		fields = slices.DeleteFunc(slices.Clone(fields), func(f serverconfig.Field) bool { return f.Owner })
	}
	m := map[string]any{
		"version":     a.Version,
		"permissions": auth.AllPerms,
		"settings":    fields,
		"groupInfo":   serverconfig.GroupInfo,
		"updates":     map[string]any{"repo": a.conf().UpdateRepo},
		// Scheduled restarts run on the manager's clock, which may not be the viewer's.
		"timeZone": managerZone(time.Now()),
	}
	// Only owners can do anything about it: updating means replacing the
	// manager on the machine it runs on.
	if rel := a.Self.Newer(); rel != nil && from(r).perms.Owner {
		m["managerUpdate"] = rel
	}
	writeJSON(w, 200, m)
}

// managerZone names the manager's time zone, as "UTC+02:00 (CEST)".
func managerZone(now time.Time) string {
	name, offset := now.Zone()
	sign := "+"
	if offset < 0 {
		sign, offset = "-", -offset
	}
	z := fmt.Sprintf("UTC%s%02d:%02d", sign, offset/3600, offset%3600/60)
	if name != "" && name != "UTC" && !strings.HasPrefix(name, "+") && !strings.HasPrefix(name, "-") {
		z += " (" + name + ")"
	}
	return z
}

// installManager puts the newer manager release in place, then restarts into
// it. Every server stops; the ones that were running start again after.
func (a *API) installManager(w http.ResponseWriter, r *http.Request) {
	if a.Restart == nil || a.Exe == "" {
		writeErr(w, 400, "this manager cannot update itself")
		return
	}
	// One at a time: a second click would back up again, install again and
	// restart twice.
	if !a.updating.TryLock() {
		writeErr(w, http.StatusConflict, "the manager is already installing an update")
		return
	}
	held := true
	defer func() {
		if held {
			a.updating.Unlock()
		}
	}()
	// The restart that follows would cut short a backup, a restore, an import
	// or a mod install.
	if a.Busy() {
		writeErr(w, http.StatusConflict, "a backup, restore, import or mod install is still running; update the manager once it's done")
		return
	}
	// Not the request's context: a closed tab must not leave a half-done swap.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Minute)
	defer cancel()
	if a.Self.Newer() == nil {
		writeErr(w, 400, "there is no newer manager release")
		return
	}
	if a.Backups != nil {
		opt := backup.Options{Kind: backup.ManagerUpdate, By: from(r).user.Username, Database: true, Servers: a.Reg.IDs()}
		if _, err := a.Backups.Create(ctx, opt); err != nil {
			a.Log.Error("backup before a manager update", "err", err)
			a.fail(w, r, 500, fmt.Errorf("could not back up first, so the manager wasn't updated: %w", err))
			return
		}
	}
	rel, err := a.Self.Install(ctx, a.Exe)
	if err != nil {
		a.fail(w, r, 400, err)
		return
	}
	a.audit(r, "", "manager.update", a.Version+" → "+rel.Version)
	writeJSON(w, 200, map[string]string{"version": rel.Version})
	// Restart once the reply has gone out; until then, it's still updating.
	held = false
	time.AfterFunc(500*time.Millisecond, a.Restart)
}

func (a *API) setup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pin      string `json:"pin"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	u, err := a.Auth.Setup(r.Context(), a.clientIP(r), strings.TrimSpace(req.Pin), strings.TrimSpace(req.Username), req.Password)
	if tooMany(w, err) {
		return
	}
	if errors.Is(err, auth.ErrSetUp) {
		a.fail(w, r, http.StatusConflict, err)
		return
	}
	if err != nil {
		a.fail(w, r, http.StatusBadRequest, err)
		return
	}
	if err := a.startSession(w, r, u); err != nil {
		a.internalErr(w, r, err)
		return
	}
	a.Log.Info("panel owner created", "username", u.Username)
	a.auditAs(r, u, "panel.setup", "")
	writeJSON(w, http.StatusOK, map[string]any{"user": u})
}

// tooMany answers 429 when err refuses an attempt for now, and reports
// whether it did.
func tooMany(w http.ResponseWriter, err error) bool {
	e, ok := errors.AsType[*auth.TooManyAttemptsError](err)
	if !ok {
		return false
	}
	w.Header().Set("Retry-After", strconv.Itoa(int(e.Wait.Seconds())+1))
	writeErr(w, http.StatusTooManyRequests, e.Error())
	return true
}

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Username)
	u, err := a.Auth.Login(r.Context(), a.clientIP(r), name, req.Password)
	if tooMany(w, err) {
		return
	}
	if errors.Is(err, auth.ErrBadLogin) {
		// A name no account could have is not worth keeping in full.
		if len(name) > 32 {
			name = name[:32] + "…"
		}
		a.auditAs(r, nil, "auth.login.failed", strconv.Quote(name))
		a.fail(w, r, http.StatusUnauthorized, err)
		return
	}
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	if err := a.startSession(w, r, u); err != nil {
		a.internalErr(w, r, err)
		return
	}
	a.auditAs(r, u, "auth.login", "password")
	writeJSON(w, http.StatusOK, map[string]any{"user": u})
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	u, token := a.sessionUser(r)
	if token != "" {
		a.Auth.EndSession(r.Context(), token)
	}
	if u != nil {
		a.auditAs(r, u, "auth.logout", "")
	}
	a.setCookie(w, r, "", -1)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *API) changePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Current string `json:"current"`
		Next    string `json:"next"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	c := from(r)
	if c.user.HasPassword {
		_, err := a.Auth.Login(r.Context(), c.ip, c.user.Username, req.Current)
		if tooMany(w, err) {
			return
		}
		if errors.Is(err, auth.ErrBadLogin) {
			writeErr(w, http.StatusBadRequest, "the current password is wrong")
			return
		}
		if err != nil {
			a.internalErr(w, r, err)
			return
		}
	}
	if _, err := a.Auth.UpdateUser(r.Context(), c.user.ID, auth.UserUpdate{Password: &req.Next}); err != nil {
		a.fail(w, r, 400, err)
		return
	}
	// The change signed the user out everywhere, here too; carry on in a new session.
	if err := a.startSession(w, r, c.user); err != nil {
		a.internalErr(w, r, err)
		return
	}
	a.audit(r, "", "auth.password.changed", "")
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---- Steam ----

const steamCookie = "rsm_steam"

// steamBase is the panel's own address as Steam sign-in uses it: where Steam
// sends the browser back to, and the site it names. It never comes from the
// Host header alone, or an assertion Steam made for some other site, returning
// to that site's address, could be handed in here under that site's name. So
// it is public_url, or else a loopback address, where only the browser's own
// machine could receive an assertion; false means Steam sign-in is off.
func (a *API) steamBase(r *http.Request) (string, bool) {
	if pub := a.conf().PublicURL; pub != "" {
		return pub, true
	}
	name := r.Host
	if h, _, err := net.SplitHostPort(name); err == nil {
		name = h
	}
	if ip, err := netip.ParseAddr(strings.Trim(name, "[]")); err == nil {
		if !ip.IsLoopback() {
			return "", false
		}
	} else if !strings.EqualFold(name, "localhost") {
		return "", false
	}
	scheme := "http"
	if a.secure(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host, true
}

// Why a Steam sign-in went back to the panel without one: the login and
// account pages show their own words for each, never text from the URL.
const (
	steamExpired     = "expired"
	steamCancelled   = "cancelled"
	steamUnreachable = "unreachable"
	steamStale       = "stale"
	steamRejected    = "rejected"
	steamTooMany     = "too-many"
	steamNoAccess    = "no-access"
	steamSignInFirst = "sign-in-first"
	steamTaken       = "taken"
	steamNoAddress   = "no-address"
	steamFailed      = "failed"
)

func (a *API) steamStart(w http.ResponseWriter, r *http.Request) {
	mode, back := "login", "/login"
	if r.URL.Query().Get("mode") == "link" {
		mode, back = "link", "/account"
	}
	base, ok := a.steamBase(r)
	if !ok {
		http.Redirect(w, r, back+"?error="+steamNoAddress, http.StatusFound)
		return
	}
	nonce := rand.Text()
	http.SetCookie(w, &http.Cookie{Name: steamCookie, Value: nonce + "." + mode, Path: "/api/auth/steam", MaxAge: 600,
		HttpOnly: true, Secure: a.secure(r), SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, a.Auth.Steam.Redirect(steamReturnTo(base, nonce), base), http.StatusFound)
}

func steamReturnTo(base, nonce string) string {
	return base + "/api/auth/steam/callback?n=" + nonce
}

func (a *API) steamCallback(w http.ResponseWriter, r *http.Request) {
	fail := func(path, code string) {
		http.Redirect(w, r, path+"?error="+code, http.StatusFound)
	}
	c, err := r.Cookie(steamCookie)
	http.SetCookie(w, &http.Cookie{Name: steamCookie, Path: "/api/auth/steam", MaxAge: -1,
		HttpOnly: true, Secure: a.secure(r), SameSite: http.SameSiteLaxMode})
	if err != nil {
		fail("/login", steamExpired)
		return
	}
	nonce, mode, _ := strings.Cut(c.Value, ".")
	back := "/login"
	if mode == "link" {
		back = "/account"
	}
	if nonce == "" || r.URL.Query().Get("n") != nonce {
		fail(back, steamExpired)
		return
	}
	base, ok := a.steamBase(r)
	if !ok {
		fail(back, steamNoAddress)
		return
	}
	steamID, err := a.Auth.SteamSignIn(r.Context(), a.clientIP(r), r.URL.Query(), steamReturnTo(base, nonce))
	if err != nil {
		a.Log.Info("steam sign-in refused", "ip", a.clientIP(r), "err", err)
		code := steamRejected
		switch _, many := errors.AsType[*auth.TooManyAttemptsError](err); {
		case many:
			code = steamTooMany
		case errors.Is(err, auth.ErrSteamCancelled):
			code = steamCancelled
		case errors.Is(err, auth.ErrSteamUnreachable):
			code = steamUnreachable
		case errors.Is(err, auth.ErrSteamStale):
			code = steamStale
		}
		fail(back, code)
		return
	}
	if mode == "link" {
		u, _ := a.sessionUser(r)
		if u == nil {
			fail("/login", steamSignInFirst)
			return
		}
		if _, err := a.Auth.UpdateUser(r.Context(), u.ID, auth.UserUpdate{SteamID: &steamID}); err != nil {
			code := steamFailed
			if errors.Is(err, auth.ErrSteamTaken) {
				code = steamTaken
			} else {
				a.Log.Error("link steam account", "user", u.Username, "err", err)
			}
			fail(back, code)
			return
		}
		// Replacing a linked account ended the user's sessions, this one too.
		if u.SteamID != "" && u.SteamID != steamID {
			if err := a.startSession(w, r, u); err != nil {
				a.Log.Error("steam link session", "err", err)
				fail("/login", steamFailed)
				return
			}
		}
		a.auditAs(r, u, "auth.steam.linked", steamID)
		a.Admins.Kick()
		http.Redirect(w, r, "/account?linked=1", http.StatusFound)
		return
	}
	u, err := a.Auth.UserBySteam(r.Context(), steamID)
	if errors.Is(err, auth.ErrNotFound) {
		a.auditAs(r, nil, "auth.login.failed", "steam "+steamID)
		fail("/login", steamNoAccess)
		return
	}
	if err == nil {
		err = a.startSession(w, r, u)
	}
	if err != nil {
		a.Log.Error("steam sign-in session", "err", err)
		fail("/login", steamFailed)
		return
	}
	a.auditAs(r, u, "auth.login", "steam")
	http.Redirect(w, r, "/", http.StatusFound)
}

func (a *API) steamUnlink(w http.ResponseWriter, r *http.Request) {
	empty := ""
	c := from(r)
	if _, err := a.Auth.UpdateUser(r.Context(), c.user.ID, auth.UserUpdate{SteamID: &empty}); err != nil {
		a.fail(w, r, errStatus(err), err)
		return
	}
	// Unlinking ended the user's sessions, this one too; carry on in a new one.
	if c.user.SteamID != "" {
		if err := a.startSession(w, r, c.user); err != nil {
			a.internalErr(w, r, err)
			return
		}
	}
	a.audit(r, "", "auth.steam.unlinked", "")
	a.Admins.Kick()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
