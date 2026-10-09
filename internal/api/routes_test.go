package api

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/go-chi/chi/v5"

	"github.com/xThrasherrr/ReSkateManager/internal/auth"
	"github.com/xThrasherrr/ReSkateManager/internal/instance"
	"github.com/xThrasherrr/ReSkateManager/internal/store"
)

// Routes anyone may call, signed in or not.
var publicRoutes = []string{
	"GET /api/health",
	"GET /api/auth/me",
	"POST /api/setup",
	"POST /api/auth/login",
	"POST /api/auth/logout",
	"GET /api/auth/steam",
	"GET /api/auth/steam/callback",
}

// Routes any signed-in user may call. Each handler keeps to what the caller
// may see or change.
var signedInRoutes = []string{
	"GET /api/meta",               // the manager update notice goes to owners only
	"GET /api/instances",          // only the servers the caller sees
	"POST /api/auth/password",     // their own
	"POST /api/auth/steam/unlink", // their own
	"GET /api/jobs/{jid}",         // only the caller's own jobs
	"GET /api/instances/{id}/ws",  // only servers the caller sees; console and roster by permission
}

// routePerms is what each other route needs: a permission, "owner", or two
// permissions joined by "+" when lacking either one turns a caller away.
// A route gated by the wrong permission fails the test as surely as one not
// gated at all. (Whether a permission must be held on all servers or on the
// one in the path, the test's users hold it on all, so can't tell apart.)
var routePerms = map[string]string{
	"DELETE /api/backups/{name}":                      "owner",
	"DELETE /api/instances/{id}/":                     "instances.manage",
	"DELETE /api/instances/{id}/admins/{steamId}":     "ingame.admins.manage",
	"DELETE /api/instances/{id}/announcements/{aid}":  "announcements.manage",
	"DELETE /api/instances/{id}/bans/{steamId}":       "players.ban",
	"DELETE /api/instances/{id}/mods/{folder}":        "settings.edit",
	"DELETE /api/roles/{rid}":                         "panel.users.manage",
	"DELETE /api/shared-mods/{folder}":                "settings.edit",
	"DELETE /api/users/{uid}":                         "panel.users.manage",
	"GET /api/audit":                                  "audit.view",
	"GET /api/backups/":                               "owner",
	"GET /api/backups/{name}":                         "owner",
	"GET /api/host":                                   "host.view",
	"GET /api/host/disk":                              "host.view",
	"GET /api/instances/{id}/admins":                  "ingame.admins.manage",
	"GET /api/instances/{id}/announcements/":          "announcements.manage",
	"GET /api/instances/{id}/backups":                 "instances.manage",
	"GET /api/instances/{id}/bans":                    "players.view",
	"GET /api/instances/{id}/export":                  "instances.manage",
	"GET /api/instances/{id}/history":                 "players.view",
	"GET /api/instances/{id}/logs":                    "console.view",
	"GET /api/instances/{id}/logs/export":             "console.view",
	"GET /api/instances/{id}/logs/files/{file}":       "console.view",
	"GET /api/instances/{id}/mods":                    "settings.view",
	"GET /api/instances/{id}/mods/installs/{jid}":     "settings.edit",
	"GET /api/instances/{id}/mods/updates":            "settings.view",
	"GET /api/instances/{id}/perf":                    "console.view",
	"GET /api/instances/{id}/players":                 "players.view",
	"GET /api/instances/{id}/settings":                "settings.view",
	"GET /api/instances/{id}/update":                  "server.update",
	"GET /api/instances/{id}/world":                   "settings.view",
	"GET /api/manager/alerts":                         "owner",
	"GET /api/manager/discord":                        "owner",
	"GET /api/manager/retention":                      "owner",
	"GET /api/manager/settings":                       "owner",
	"GET /api/roles":                                  "panel.users.manage",
	"GET /api/shared-mods/":                           "settings.view",
	"GET /api/shared-mods/installs/{jid}":             "settings.edit",
	"GET /api/shared-mods/updates":                    "settings.view",
	"GET /api/thunderstore/maps":                      "settings.view",
	"GET /api/thunderstore/packages":                  "settings.view",
	"GET /api/users":                                  "panel.users.manage",
	"PATCH /api/backups/":                             "owner",
	"PATCH /api/instances/{id}/":                      "instances.manage",
	"PATCH /api/instances/{id}/announcements/{aid}":   "announcements.manage",
	"PATCH /api/instances/{id}/mods/{folder}":         "settings.edit",
	"PATCH /api/instances/{id}/shared-mods":           "settings.edit",
	"PATCH /api/manager/alerts":                       "owner",
	"PATCH /api/manager/discord":                      "owner",
	"PATCH /api/manager/retention":                    "owner",
	"PATCH /api/manager/settings":                     "owner",
	"PATCH /api/roles/{rid}":                          "panel.users.manage",
	"PATCH /api/users/{uid}":                          "panel.users.manage",
	"POST /api/backups/":                              "owner",
	"POST /api/imports":                               "instances.manage",
	"POST /api/instances":                             "instances.manage",
	"POST /api/instances/{id}/admins":                 "ingame.admins.manage",
	"POST /api/instances/{id}/announcements/":         "announcements.manage",
	"POST /api/instances/{id}/bans":                   "players.ban",
	"POST /api/instances/{id}/command":                "console.exec",
	"POST /api/instances/{id}/kill":                   "server.lifecycle",
	"POST /api/instances/{id}/mods/{folder}/share":    "settings.edit",
	"POST /api/instances/{id}/mods/{folder}/update":   "settings.edit",
	"POST /api/instances/{id}/mods/installs":          "settings.edit",
	"POST /api/instances/{id}/mods/updates":           "settings.edit",
	"POST /api/instances/{id}/mods/uploads":           "settings.edit",
	"POST /api/instances/{id}/players/{steamId}/kick": "players.kick",
	"POST /api/instances/{id}/restart":                "server.lifecycle",
	"POST /api/instances/{id}/restore":                "instances.manage",
	"POST /api/instances/{id}/say":                    "players.chat",
	"POST /api/instances/{id}/settings":               "settings.edit",
	"POST /api/instances/{id}/start":                  "server.lifecycle",
	"POST /api/instances/{id}/stop":                   "server.lifecycle",
	"POST /api/instances/{id}/update":                 "server.update",
	"POST /api/instances/{id}/world":                  "settings.edit",
	"POST /api/manager/alerts/test":                   "owner",
	"POST /api/manager/discord/test":                  "owner",
	"POST /api/manager/update":                        "owner",
	"POST /api/roles":                                 "panel.users.manage",
	"POST /api/shared-mods/{folder}/update":           "settings.edit",
	"POST /api/shared-mods/installs":                  "settings.edit",
	"POST /api/shared-mods/updates":                   "settings.edit",
	"POST /api/shared-mods/uploads":                   "settings.edit",
	"POST /api/users":                                 "panel.users.manage",
	"PUT /api/imports/{uid}":                          "instances.manage",
	"PUT /api/instances/{id}/mods/uploads/{uid}":      "settings.edit",
	"PUT /api/shared-mods/uploads/{uid}":              "settings.edit",
}

// Mutating routes that write no audit entry, and why.
var unauditedRoutes = map[string]string{
	"POST /api/instances/{id}/mods/uploads": "only reserves an upload; its last chunk writes mod.upload",
	"POST /api/shared-mods/uploads":         "only reserves an upload; its last chunk writes shared.upload",
	"POST /api/imports":                     "only reserves an upload; the import its last chunk starts writes instance.import",
}

// route is one method and pattern from the router, with the middleware in
// front of its handler.
type route struct {
	key     string // "POST /api/instances"
	handler http.Handler
	mws     []func(http.Handler) http.Handler
}

func apiRoutes(t *testing.T, a *API) []route {
	t.Helper()
	var out []route
	err := chi.Walk(a.Handler().(chi.Routes), func(method, pattern string, h http.Handler, mws ...func(http.Handler) http.Handler) error {
		if strings.HasPrefix(pattern, "/api/") {
			// Cloned: chi appends each method's middleware to one shared
			// slice, so the next method on this path would overwrite it.
			out = append(out, route{method + " " + pattern, h, slices.Clone(mws)})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatal("walked no API routes")
	}
	return out
}

var paramRe = regexp.MustCompile(`\{([^}:]+)`)

// TestRoutesNeedPermission fails on any API route that lets a caller through
// without a permission check, unless it's listed above. Each route's
// middleware runs in front of a stand-in handler, for nobody and for users
// who hold every permission but one on every server. A route is gated when
// one of them is turned away.
func TestRoutesNeedPermission(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	svc, err := auth.New(ctx, st.DB)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	reg := instance.NewRegistry(log)
	reg.Add(instance.Def{ID: "a", Name: "a", Dir: t.TempDir()})
	a := &API{Store: st, Auth: svc, Reg: reg, Log: log, Static: fstest.MapFS{}, SharedDir: t.TempDir()}

	tokens := map[string]string{} // missing permission -> session
	for i, p := range auth.AllPerms {
		var rest []string
		for _, q := range auth.AllPerms {
			if q.Key != p.Key {
				rest = append(rest, q.Key)
			}
		}
		role, err := svc.SaveRole(ctx, auth.Role{Name: "all but " + p.Key, Permissions: rest})
		if err != nil {
			t.Fatal(err)
		}
		// A Steam ID instead of a password skips the slow hash.
		u, err := svc.CreateUser(ctx, auth.NewUser{Username: "no-" + p.Key, SteamID: fmt.Sprintf("76561198%09d", i)})
		if err != nil {
			t.Fatal(err)
		}
		grants := []auth.Grant{{RoleID: role.ID, Instance: "*"}}
		if _, err := svc.UpdateUser(ctx, u.ID, auth.UserUpdate{Grants: &grants}); err != nil {
			t.Fatal(err)
		}
		if tokens[p.Key], err = svc.NewSession(ctx, u.ID, "", ""); err != nil {
			t.Fatal(err)
		}
	}

	// reached reports whether a request with this session gets past the
	// route's middleware.
	reached := func(rt route, token string) bool {
		var hit bool
		h := chi.Chain(rt.mws...).HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true })
		method, pattern, _ := strings.Cut(rt.key, " ")
		req := httptest.NewRequest(method, "/", nil)
		req.Header.Set("X-RSM", "1")
		if token != "" {
			req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: token})
		}
		rctx := chi.NewRouteContext()
		for _, m := range paramRe.FindAllStringSubmatch(pattern, -1) {
			rctx.URLParams.Add(m[1], "a") // {id} is server a; the stand-in reads no others
		}
		h.ServeHTTP(httptest.NewRecorder(), req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx)))
		return hit
	}

	walked := map[string]bool{}
	for _, rt := range apiRoutes(t, a) {
		walked[rt.key] = true
		got := "public"
		if !reached(rt, "") {
			// Which permission, missing, turns a caller away. Missing any one
			// at all means only owners get through.
			var needs []string
			for _, p := range auth.AllPerms {
				if !reached(rt, tokens[p.Key]) {
					needs = append(needs, p.Key)
				}
			}
			switch len(needs) {
			case 0:
				got = "signed in"
			case len(auth.AllPerms):
				got = "owner"
			default:
				got = strings.Join(needs, "+")
			}
		}
		want, ok := routePerms[rt.key]
		switch {
		case slices.Contains(publicRoutes, rt.key):
			want, ok = "public", true
		case slices.Contains(signedInRoutes, rt.key):
			want, ok = "signed in", true
		}
		if !ok {
			t.Errorf("%q: %q, is new: say in routePerms what it needs", rt.key, got)
			continue
		}
		if got != want {
			t.Errorf("%s needs %s, want %s", rt.key, got, want)
		}
	}
	for _, k := range slices.Concat(publicRoutes, signedInRoutes, slices.Collect(maps.Keys(routePerms))) {
		if !walked[k] {
			t.Errorf("%s is listed but the router has no such route", k)
		}
	}
}

// TestMutatingRoutesAudit fails on a POST, PUT, PATCH or DELETE route whose
// handler never writes an audit entry, unless it's listed in unauditedRoutes.
// It reads this package's source: a handler counts when it, or a function
// here it calls, calls Audit.
func TestMutatingRoutesAudit(t *testing.T) {
	audits := auditingFuncs(t)
	walked := map[string]bool{}
	for _, rt := range apiRoutes(t, &API{Static: fstest.MapFS{}}) {
		method, _, _ := strings.Cut(rt.key, " ")
		if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions {
			continue
		}
		walked[rt.key] = true
		fn, symbol := handlerFunc(rt.handler, audits)
		_, exempt := unauditedRoutes[rt.key]
		switch {
		case fn == "":
			t.Errorf("%s: can't find the handler %q in this package", rt.key, symbol)
		case audits[fn] && exempt:
			t.Errorf("%s writes an audit entry now; take it out of unauditedRoutes", rt.key)
		case !audits[fn] && !exempt:
			t.Errorf("%s: %s writes no audit entry; call a.audit, or list the route in unauditedRoutes with why", rt.key, fn)
		}
	}
	for k := range unauditedRoutes {
		if !walked[k] {
			t.Errorf("%s is listed in unauditedRoutes but the router has no such mutating route", k)
		}
	}
}

// handlerFunc names the function in this package a route's handler comes
// from: a method such as createInstance, or the one that built the closure,
// such as lifecycle. funcs holds every function name in the package.
func handlerFunc(h http.Handler, funcs map[string]bool) (fn, symbol string) {
	v := reflect.ValueOf(h)
	if v.Kind() != reflect.Func {
		return "", fmt.Sprintf("%T", h)
	}
	symbol = runtime.FuncForPC(v.Pointer()).Name() // ".../api.(*API).createInstance-fm", ".../api.(*API).lifecycle.func1"
	parts := strings.Split(strings.TrimSuffix(symbol[strings.LastIndexByte(symbol, '/')+1:], "-fm"), ".")
	for _, p := range slices.Backward(parts) {
		if _, ok := funcs[p]; ok {
			return p, symbol
		}
	}
	return "", symbol
}

// auditingFuncs maps every function and method in this package (tests aside)
// to whether it writes an audit entry, itself or through a function it calls.
// Calls are matched by name only, which errs towards counting a call.
func auditingFuncs(t *testing.T) map[string]bool {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	calls := map[string][]string{}
	audits := map[string]bool{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			fn := fd.Name.Name
			if _, ok := audits[fn]; !ok {
				audits[fn] = false
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch f := call.Fun.(type) {
				case *ast.Ident:
					calls[fn] = append(calls[fn], f.Name)
				case *ast.SelectorExpr:
					calls[fn] = append(calls[fn], f.Sel.Name)
					if f.Sel.Name == "Audit" {
						audits[fn] = true
					}
				}
				return true
			})
		}
	}
	for changed := true; changed; {
		changed = false
		for fn, callees := range calls {
			if !audits[fn] && slices.ContainsFunc(callees, func(c string) bool { return audits[c] }) {
				audits[fn], changed = true, true
			}
		}
	}
	return audits
}
