// Package api is the panel's HTTP and WebSocket interface.
package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"modernc.org/sqlite"

	"github.com/xThrasherrr/ReSkateManager/internal/adminsync"
	"github.com/xThrasherrr/ReSkateManager/internal/alerts"
	"github.com/xThrasherrr/ReSkateManager/internal/auth"
	"github.com/xThrasherrr/ReSkateManager/internal/backup"
	"github.com/xThrasherrr/ReSkateManager/internal/config"
	"github.com/xThrasherrr/ReSkateManager/internal/instance"
	"github.com/xThrasherrr/ReSkateManager/internal/store"
	"github.com/xThrasherrr/ReSkateManager/internal/thunderstore"
	"github.com/xThrasherrr/ReSkateManager/internal/updater"
)

// API is the panel: its HTTP and WebSocket handlers and what they work on.
// Set the exported fields, then serve Handler or Server.
type API struct {
	Cfg        config.Config // as loaded; read through conf, which sees panel changes
	ConfigPath string        // manager.toml, where panel changes are saved
	Store      *store.Store
	Auth       *auth.Service
	Reg        *instance.Registry
	Updates    *updater.Service
	Builds     *updater.Builds   // tells which release each server's program is from
	Admins     *adminsync.Syncer // nil: no in-game admin sync
	Self       *updater.SelfChecker
	Log        *slog.Logger
	Version    string
	ServersDir string
	// SharedDir holds the shared mods, in its Mods folder; empty turns them off.
	SharedDir string
	DataDir   string // the database, manager.toml and the manager's log
	CacheDir  string // downloaded server releases
	Static    fs.FS
	Exe       string // the manager binary a manager update replaces
	Restart   func() // stops every server and starts the manager again
	// Base ends when the manager shuts down, and with it the work the panel
	// started in the background: backups, restores, imports, mod installs.
	// Nil never ends. Wait waits for that work to stop.
	Base context.Context

	// Thunderstore is where the panel browses mods, installs them from and
	// looks up newer versions of installed ones; nil turns all that off.
	Thunderstore *thunderstore.Client
	// Alerts sends the test alert from the Manager page; nil turns that off.
	Alerts *alerts.Notifier
	// Backups makes and lists the backups, and restores servers from them;
	// nil turns them off.
	Backups *backup.Service

	cfgMu sync.Mutex // one settings change at a time
	modMu sync.Mutex // one change to a Mods folder at a time
	// instMu holds one change at a time to which servers there are and to
	// their settings in the database, so two can't both take a name, a folder
	// or ports, nor one overwrite the other's change.
	instMu     sync.Mutex
	reserved   map[string]bool // IDs taken by imports under way; instMu guards it
	upMu       sync.Mutex
	modUploads map[string]*modUpload // mod zips arriving in chunks, by id
	modJobs    jobTable[*modJob]     // mods being installed from Thunderstore
	probeMu    sync.Mutex
	probes     map[string]*mapsProbe // looks inside Thunderstore zips, by version
	probeSem   chan struct{}
	jobs       jobTable[*job] // backups, restores and imports
	diskMu     sync.Mutex
	disk       *diskMeasure // the last measurement of the manager's folder
	diskBusy   bool         // one is running
	live       atomic.Pointer[config.Config]
	// proxyWarned is set once a proxy's header came from somewhere else.
	proxyWarned atomic.Bool
	work        sync.WaitGroup // background work, for Wait
	working     atomic.Int32   // background work still running
	updating    sync.Mutex     // held while the manager installs an update of itself
	sockMu      sync.Mutex
	sockets     map[int64]int // console sockets open, by user
	scansOnce   sync.Once
	scans       chan struct{} // log scans running
}

// conf is the configuration in effect: Cfg with any changes made in the panel.
func (a *API) conf() config.Config {
	if c := a.live.Load(); c != nil {
		return *c
	}
	return a.Cfg
}

type ctxKey struct{}

type caller struct {
	user  *auth.User
	perms auth.Perms
	ip    string
}

func from(r *http.Request) *caller {
	c, _ := r.Context().Value(ctxKey{}).(*caller)
	return c
}

// Handler routes the API under /api and the panel's files everywhere else.
func (a *API) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(a.securityHeaders)

	r.Route("/api", func(r chi.Router) {
		r.Use(noStore)
		r.Use(a.csrf)
		r.Get("/health", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
		r.Get("/auth/me", a.me)
		r.Post("/setup", a.setup)
		r.Post("/auth/login", a.login)
		r.Post("/auth/logout", a.logout)
		r.Get("/auth/steam", a.steamStart)
		r.Get("/auth/steam/callback", a.steamCallback)

		r.Group(func(r chi.Router) {
			r.Use(a.requireUser)
			r.Get("/meta", a.meta)
			r.With(a.ownerOnly).Post("/manager/update", a.installManager)
			r.With(a.ownerOnly).Get("/manager/settings", a.managerSettings)
			r.With(a.ownerOnly).Patch("/manager/settings", a.saveManagerSettings)
			r.With(a.ownerOnly).Get("/manager/alerts", a.alertSettings)
			r.With(a.ownerOnly).Patch("/manager/alerts", a.saveAlertSettings)
			r.With(a.ownerOnly).Post("/manager/alerts/test", a.testAlert)
			r.With(a.ownerOnly).Get("/manager/retention", a.retention)
			r.With(a.ownerOnly).Patch("/manager/retention", a.saveRetention)
			r.Post("/auth/password", a.changePassword)
			r.Post("/auth/steam/unlink", a.steamUnlink)
			r.Get("/jobs/{jid}", a.jobStatus)

			r.Route("/backups", func(r chi.Router) {
				r.Use(a.ownerOnly)
				r.Get("/", a.backups)
				r.Post("/", a.createBackup)
				r.Patch("/", a.saveBackupSettings)
				r.Get("/{name}", a.downloadBackup)
				r.Delete("/{name}", a.deleteBackup)
			})
			// Importing a server goes to a new folder in servers/, as creating one does.
			r.With(a.global(auth.InstancesManage)).Post("/imports", a.startImport)
			r.With(a.global(auth.InstancesManage)).Put("/imports/{uid}", a.importChunk)

			r.Get("/instances", a.listInstances)
			r.With(a.global(auth.InstancesManage)).Post("/instances", a.createInstance)
			r.Route("/instances/{id}", func(r chi.Router) {
				r.Use(a.loadInstance)
				r.With(a.can(auth.InstancesManage)).Patch("/", a.updateInstance)
				r.With(a.can(auth.InstancesManage)).Delete("/", a.deleteInstance)
				r.With(a.can(auth.ServerLifecycle)).Post("/start", a.lifecycle("start"))
				r.With(a.can(auth.ServerLifecycle)).Post("/stop", a.lifecycle("stop"))
				r.With(a.can(auth.ServerLifecycle)).Post("/restart", a.lifecycle("restart"))
				r.With(a.can(auth.ServerLifecycle)).Post("/kill", a.lifecycle("kill"))
				r.With(a.can(auth.ConsoleExec)).Post("/command", a.command)
				r.Get("/ws", a.ws) // what it sends depends on the caller's permissions
				r.With(a.can(auth.ConsoleView)).Get("/perf", a.performance)
				r.With(a.can(auth.ConsoleView)).Get("/logs", a.logs)
				r.With(a.can(auth.ConsoleView)).Get("/logs/export", a.exportLogs)
				r.With(a.can(auth.ConsoleView)).Get("/logs/files/{file}", a.downloadLog)

				r.With(a.can(auth.PlayersView)).Get("/players", a.players)
				r.With(a.can(auth.PlayersView)).Get("/history", a.history)
				r.With(a.can(auth.PlayersKick)).Post("/players/{steamId}/kick", a.kick)
				r.With(a.can(auth.PlayersView)).Get("/bans", a.bans)
				r.With(a.can(auth.PlayersBan)).Post("/bans", a.ban)
				r.With(a.can(auth.PlayersBan)).Delete("/bans/{steamId}", a.unban)
				r.With(a.can(auth.PlayersChat)).Post("/say", a.say)
				r.Route("/announcements", func(r chi.Router) {
					r.Use(a.can(auth.Announcements))
					r.Get("/", a.announcements)
					r.Post("/", a.addAnnouncement)
					r.Patch("/{aid}", a.updateAnnouncement)
					r.Delete("/{aid}", a.deleteAnnouncement)
				})

				r.With(a.can(auth.SettingsView)).Get("/settings", a.settings)
				r.With(a.can(auth.SettingsView)).Get("/mods", a.mods)
				r.With(a.can(auth.SettingsEdit)).Post("/mods/uploads", a.startModUpload)
				r.With(a.can(auth.SettingsEdit)).Put("/mods/uploads/{uid}", a.modUploadChunk)
				r.With(a.can(auth.SettingsView)).Get("/mods/updates", a.modUpdates)
				r.With(a.can(auth.SettingsEdit)).Post("/mods/updates", a.updateAllMods)
				r.With(a.can(auth.SettingsEdit)).Post("/mods/installs", a.installMod)
				r.With(a.can(auth.SettingsEdit)).Get("/mods/installs/{jid}", a.installStatus)
				r.With(a.can(auth.SettingsEdit)).Patch("/mods/{folder}", a.setModEnabled)
				r.With(a.can(auth.SettingsEdit)).Delete("/mods/{folder}", a.deleteMod)
				r.With(a.can(auth.SettingsEdit)).Post("/mods/{folder}/update", a.updateMod)
				r.With(a.global(auth.SettingsEdit), a.sharedOn).Post("/mods/{folder}/share", a.shareMod)
				r.With(a.can(auth.SettingsEdit), a.sharedOn).Patch("/shared-mods", a.setSharedMods)
				r.With(a.can(auth.SettingsView)).Get("/world", a.world)
				r.With(a.can(auth.SettingsEdit)).Post("/world", a.saveWorld)
				r.With(a.can(auth.SettingsEdit)).Post("/settings", a.saveSettings)
				r.With(a.can(auth.IngameAdmins)).Get("/admins", a.admins)
				r.With(a.can(auth.IngameAdmins)).Post("/admins", a.addAdmin)
				r.With(a.can(auth.IngameAdmins)).Delete("/admins/{steamId}", a.removeAdmin)

				r.With(a.can(auth.ServerUpdate)).Get("/update", a.updateStatus)
				r.With(a.can(auth.ServerUpdate)).Post("/update", a.startUpdate)

				r.With(a.can(auth.InstancesManage)).Get("/backups", a.serverBackups)
				r.With(a.can(auth.InstancesManage)).Post("/restore", a.restoreServer)
				r.With(a.can(auth.InstancesManage)).Get("/export", a.exportServer)
			})

			r.Group(func(r chi.Router) {
				r.Use(a.global(auth.PanelUsersManage))
				r.Get("/users", a.listUsers)
				r.Post("/users", a.createUser)
				r.Patch("/users/{uid}", a.updateUser)
				r.Delete("/users/{uid}", a.deleteUser)
				r.Get("/roles", a.listRoles)
				r.Post("/roles", a.saveRole)
				r.Patch("/roles/{rid}", a.saveRole)
				r.Delete("/roles/{rid}", a.deleteRole)
			})
			r.With(a.global(auth.AuditView)).Get("/audit", a.auditLog)
			r.With(a.global(auth.HostView)).Get("/host", a.hostPerformance)
			r.With(a.global(auth.HostView)).Get("/host/disk", a.hostDisk)

			// The shared mods reach every server that uses them, so changing
			// them takes settings.edit on all servers.
			r.Route("/shared-mods", func(r chi.Router) {
				r.Use(a.sharedOn)
				r.With(a.anyServer(auth.SettingsView)).Get("/", a.sharedMods)
				r.With(a.anyServer(auth.SettingsView)).Get("/updates", a.sharedModUpdates)
				r.With(a.global(auth.SettingsEdit)).Post("/updates", a.updateAllSharedMods)
				r.With(a.global(auth.SettingsEdit)).Post("/uploads", a.startSharedModUpload)
				r.With(a.global(auth.SettingsEdit)).Put("/uploads/{uid}", a.sharedModUploadChunk)
				r.With(a.global(auth.SettingsEdit)).Post("/installs", a.installSharedMod)
				r.With(a.global(auth.SettingsEdit)).Get("/installs/{jid}", a.sharedInstallStatus)
				r.With(a.global(auth.SettingsEdit)).Delete("/{folder}", a.deleteSharedMod)
				r.With(a.global(auth.SettingsEdit)).Post("/{folder}/update", a.updateSharedMod)
			})

			// Browsing Thunderstore; installing goes through a server's mods or the shared ones.
			r.Route("/thunderstore", func(r chi.Router) {
				r.Use(a.anyServer(auth.SettingsView))
				r.Get("/packages", a.browseMods)
				r.Get("/maps", a.browseModMaps)
			})
		})
		r.NotFound(func(w http.ResponseWriter, r *http.Request) { writeErr(w, 404, "not found") })
	})

	r.Handle("/*", a.spa())
	return r
}

// spa serves the built panel, falling back to index.html for client routes.
func (a *API) spa() http.Handler {
	files := http.FileServer(http.FS(a.Static))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" {
			if st, err := fs.Stat(a.Static, p); err == nil && !st.IsDir() {
				if strings.HasPrefix(p, "_app/immutable/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				} else {
					// favicon.png and the like keep their names across releases. Left
					// unmarked, Cloudflare and browsers hold the old copy for hours.
					w.Header().Set("Cache-Control", "no-cache")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		index, err := fs.ReadFile(a.Static, "index.html")
		if err != nil {
			http.Error(w, "The panel was built without its web UI. Run `task build`.", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(index)
	})
}

func (a *API) securityHeaders(next http.Handler) http.Handler {
	csp := contentSecurityPolicy(a.Static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Content-Security-Policy", csp)
		// Browsers heed it only over HTTPS, and then refuse plain HTTP to this
		// host, so no one on the network can downgrade a visit and read the cookie.
		if a.secure(r) {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

var inlineScriptRe = regexp.MustCompile(`(?is)<script\b[^>]*>(.*?)</script>`)

// contentSecurityPolicy allows the inline scripts in the panel's index.html by
// their SHA-256 (SvelteKit starts the app from one) and no other: a script
// that reached the page through a bug would not run. Styles stay inline, as
// Svelte sets them on elements.
func contentSecurityPolicy(static fs.FS) string {
	var scripts strings.Builder
	scripts.WriteString("'self'")
	if static != nil {
		index, _ := fs.ReadFile(static, "index.html")
		for _, m := range inlineScriptRe.FindAllSubmatch(index, -1) {
			if len(m[1]) == 0 {
				continue // a <script src>
			}
			// Browsers hash the text after HTML parsing, which makes every line break \n.
			body := bytes.ReplaceAll(m[1], []byte("\r\n"), []byte("\n"))
			body = bytes.ReplaceAll(body, []byte("\r"), []byte("\n"))
			sum := sha256.Sum256(body)
			scripts.WriteString(" 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'")
		}
	}
	// Images also come from Steam (avatars) and Thunderstore (mod icons).
	// base-uri and form-action aren't covered by default-src: without them a
	// <base> or <form> slipped into the page could send its links or a form
	// elsewhere.
	return "default-src 'self'; img-src 'self' data: https://avatars.steamstatic.com https://ccdn.thunderstore.io https://gcdn.thunderstore.io; style-src 'self' 'unsafe-inline'; " +
		"script-src " + scripts.String() + "; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"
}

// noStore keeps every API answer out of caches. Behind Cloudflare it matters:
// Cloudflare caches a URL that ends in .zip, such as a backup's download,
// when the answer doesn't say otherwise, and hands its copy to anyone who asks
// for that URL, signed in or not.
func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// csrf: state-changing requests must carry X-RSM, a header a cross-site form
// or a simple fetch cannot send without a CORS preflight this server never allows.
func (a *API) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if r.Header.Get("X-RSM") != "1" {
				writeErr(w, http.StatusForbidden, "missing request header")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	// Only the configured proxy's header: any other one reaches us unchanged
	// from the visitor, who can put any address in it.
	var name string
	proxy := a.conf().Proxy
	switch proxy {
	case config.ProxyCloudflare:
		name = "Cf-Connecting-IP"
	case config.ProxyForwarded:
		name = "X-Forwarded-For"
	}
	vals := r.Header.Values(name)
	if name == "" || len(vals) == 0 {
		return host
	}
	// And only from the proxy itself. The port should be closed to everyone
	// else, but if it isn't, a visitor reaching it directly would otherwise
	// pick their own address, and with it their own sign-in limits.
	if peer, err := netip.ParseAddr(host); err != nil || !fromProxy(peer, proxy) {
		if !a.proxyWarned.Swap(true) {
			a.Log.Warn("a request came straight to the panel's port, not through the proxy; its "+name+" header is ignored. "+
				"Only the proxy should be able to reach the port", "from", host, "proxy", proxy)
		}
		return host
	}
	// Proxies append, so only the last entry is theirs; anything before it
	// came from the client and can be forged.
	last := vals[len(vals)-1]
	if i := strings.LastIndexByte(last, ','); i >= 0 {
		last = last[i+1:]
	}
	if ip, err := netip.ParseAddr(strings.TrimSpace(last)); err == nil {
		return ip.Unmap().String()
	}
	return host
}

// fromProxy reports whether a request from peer can have come through the
// named proxy: one on this machine or a private network (nginx next door, a
// Cloudflare Tunnel, a container beside this one, a Tailscale peer) or, for
// Cloudflare, one of its edge servers.
func fromProxy(peer netip.Addr, proxy string) bool {
	peer = peer.Unmap()
	if peer.IsLoopback() || peer.IsPrivate() || peer.IsLinkLocalUnicast() || sharedNet.Contains(peer) {
		return true
	}
	if proxy != config.ProxyCloudflare {
		return false
	}
	for _, p := range cloudflareNets {
		if p.Contains(peer) {
			return true
		}
	}
	return false
}

// sharedNet is the carrier-grade NAT block Tailscale hands out addresses from.
var sharedNet = netip.MustParsePrefix("100.64.0.0/10")

// cloudflareNets are Cloudflare's edge addresses, from cloudflare.com/ips.
// They seldom change; a request from one missing here counts as coming from
// that edge server, which only merges visitors' sign-in limits.
var cloudflareNets = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22", "141.101.64.0/18",
		"108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20", "197.234.240.0/22", "198.41.128.0/17",
		"162.158.0.0/15", "104.16.0.0/13", "104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
		"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32", "2405:8100::/32",
		"2a06:98c0::/29", "2c0f:f248::/32",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

func (a *API) secure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	c := a.conf()
	if c.Proxy != config.ProxyNone && r.Header.Get("X-Forwarded-Proto") == "https" {
		return true
	}
	// Only a request to the HTTPS public address itself: one to a LAN address
	// came over plain HTTP, where browsers refuse a Secure cookie and the
	// sign-in would never stick.
	u, err := url.Parse(c.PublicURL)
	return err == nil && u.Scheme == "https" && strings.EqualFold(u.Host, r.Host)
}

func (a *API) sessionUser(r *http.Request) (*auth.User, string) {
	c, err := r.Cookie(auth.SessionCookie)
	if err != nil {
		return nil, ""
	}
	u, err := a.Auth.Session(r.Context(), c.Value)
	if err != nil {
		return nil, c.Value
	}
	return u, c.Value
}

func (a *API) requireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, _ := a.sessionUser(r)
		if u == nil {
			writeErr(w, http.StatusUnauthorized, "sign in first")
			return
		}
		perms, err := a.Auth.Perms(r.Context(), u)
		if err != nil {
			a.internalErr(w, r, err)
			return
		}
		ctx := context.WithValue(r.Context(), ctxKey{}, &caller{user: u, perms: perms, ip: a.clientIP(r)})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (a *API) ownerOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !from(r).perms.Owner {
			writeErr(w, http.StatusForbidden, "only owners can do that")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) global(perm string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !from(r).perms.Can(perm, "") {
				writeErr(w, http.StatusForbidden, "you don't have permission for that")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// anyServer lets through a user who holds perm on at least one server.
func (a *API) anyServer(perm string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !from(r).perms.CanAny(perm) {
				writeErr(w, http.StatusForbidden, "you don't have permission for that")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// sharedOn answers 404 for the shared mods' routes in a manager that has
// them turned off.
func (a *API) sharedOn(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.SharedDir == "" {
			writeErr(w, http.StatusNotFound, errSharedOff.Error())
			return
		}
		next.ServeHTTP(w, r)
	})
}

type instKey struct{}

func inst(r *http.Request) *instance.Instance {
	return r.Context().Value(instKey{}).(*instance.Instance)
}

func (a *API) loadInstance(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		in, ok := a.Reg.Get(id)
		c := from(r)
		// Hide instances the caller has no access to at all.
		if !ok || !c.perms.Sees(id) {
			writeErr(w, http.StatusNotFound, "no such server")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), instKey{}, in)))
	})
}

func (a *API) can(perm string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !from(r).perms.Can(perm, inst(r).ID()) {
				writeErr(w, http.StatusForbidden, "you don't have permission for that")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// audit records what the signed-in caller did.
func (a *API) audit(r *http.Request, instanceID, action, detail string) {
	c := from(r)
	e := store.AuditEntry{Instance: instanceID, Action: action, Detail: detail}
	if c != nil {
		e.UserID, e.Username, e.IP = c.user.ID, c.user.Username, c.ip
	} else {
		e.IP = a.clientIP(r)
	}
	a.writeAudit(r, e)
}

// auditAs records what u did (nil: someone not signed in) on a route that
// has no signed-in caller, such as signing in.
func (a *API) auditAs(r *http.Request, u *auth.User, action, detail string) {
	a.writeAudit(r, auditEntry(u, a.clientIP(r), "", action, detail))
}

// auditLater returns what writes the caller's audit entry for action, once
// whatever r started in the background has finished and r is long answered.
func (a *API) auditLater(r *http.Request, instanceID, action string) func(detail string) {
	write := a.auditLaterFor(r, action)
	return func(detail string) { write(instanceID, detail) }
}

// auditLaterFor is auditLater for work that learns its server as it goes,
// such as an import.
func (a *API) auditLaterFor(r *http.Request, action string) func(instanceID, detail string) {
	c := from(r)
	return func(instanceID, detail string) {
		if err := a.Store.Audit(context.Background(), auditEntry(c.user, c.ip, instanceID, action, detail)); err != nil {
			a.Log.Warn("audit write failed", "err", err)
		}
	}
}

// writeAudit outlives the request: a closed tab must not lose the entry.
func (a *API) writeAudit(r *http.Request, e store.AuditEntry) {
	if err := a.Store.Audit(context.WithoutCancel(r.Context()), e); err != nil {
		a.Log.Warn("audit write failed", "err", err)
	}
}

// ---- JSON helpers ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// internalErr logs err and answers 500 without its text, which can hold file
// paths or SQL.
func (a *API) internalErr(w http.ResponseWriter, r *http.Request, err error) {
	a.Log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	writeErr(w, http.StatusInternalServerError, "something went wrong; the manager log has the details")
}

// fail answers err with status. An error from the system rather than the
// request (a file, the database, starting a program) is shown to owners, who
// run the machine, and to anyone else only as "see the log": its text can
// hold paths or SQL.
func (a *API) fail(w http.ResponseWriter, r *http.Request, status int, err error) {
	if systemErr(err) && !isOwner(r) {
		a.internalErr(w, r, err)
		return
	}
	writeErr(w, status, err.Error())
}

// failText is what a job that failed with err says to whoever started it,
// as fail would answer it.
func (a *API) failText(owner bool, err error) string {
	if systemErr(err) && !owner {
		a.Log.Error("background work failed", "err", err)
		return "it failed; the manager log has the details"
	}
	return err.Error()
}

func isOwner(r *http.Request) bool {
	c := from(r)
	return c != nil && c.perms.Owner
}

// systemErr reports whether err came from the system rather than the
// request: a file, the database, or a program that wouldn't start.
func systemErr(err error) bool {
	if _, ok := errors.AsType[*fs.PathError](err); ok {
		return true
	}
	if _, ok := errors.AsType[*os.LinkError](err); ok {
		return true
	}
	if _, ok := errors.AsType[*os.SyscallError](err); ok {
		return true
	}
	if _, ok := errors.AsType[*exec.Error](err); ok {
		return true
	}
	_, ok := errors.AsType[*sqlite.Error](err)
	return ok
}

// attachment marks the answer as a file to save under name, quoted as
// Content-Disposition needs whatever the name holds.
func attachment(w http.ResponseWriter, name string) {
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
}

// wholeDownload reports whether a request for a file starts at its first byte:
// a plain download, or the first part of one. The later parts of a resumed
// download aren't written down again, but a Range can't skip the audit log.
func wholeDownload(r *http.Request) bool {
	rng := strings.TrimSpace(r.Header.Get("Range"))
	return rng == "" || strings.HasPrefix(rng, "bytes=0-") || strings.Contains(rng, ",")
}

// pathParam is {key} in the request's path, decoded. chi hands over the path
// as sent only when it holds escapes Go wouldn't write itself; otherwise it is
// decoded already, and decoding it again would turn a folder named "50%" into
// an error and "a%2Bb" into another folder.
func pathParam(r *http.Request, key string) (string, error) {
	v := chi.URLParam(r, key)
	if r.URL.RawPath == "" {
		return v, nil
	}
	return url.PathUnescape(v)
}

// readJSON decodes the request's body, one JSON object of at most 1 MB with
// no fields v lacks, into v; or answers why not.
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	if err == nil {
		// Only space may follow: not junk, nor a second object.
		if _, terr := dec.Token(); !errors.Is(terr, io.EOF) {
			err = errors.New("more follows the JSON object")
		}
	}
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		writeErr(w, http.StatusRequestEntityTooLarge, "the request is over 1 MB")
		return false
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad request: "+err.Error())
		return false
	}
	return true
}

// errStatus is the status for an error from a request to change something:
// 404 for what isn't there, 409 for what is in the way, 400 otherwise.
func errStatus(err error) int {
	switch {
	case errors.Is(err, auth.ErrNotFound), errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, instance.ErrBusy), errors.Is(err, updater.ErrRunning), errors.Is(err, auth.ErrLastOwner),
		errors.Is(err, auth.ErrSetUp), errors.Is(err, auth.ErrSteamTaken):
		return http.StatusConflict
	}
	return http.StatusBadRequest
}

// Server wraps the handler with sane timeouts. There is no write timeout:
// installing a manager update or a mod from Thunderstore answers only after a
// download that can take minutes. What net/http logs (a TLS handshake that
// failed, say) goes to the manager's log.
func (a *API) Server() *http.Server {
	return &http.Server{
		Addr:              a.conf().Listen,
		Handler:           a.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(a.Log.Handler(), slog.LevelWarn),
	}
}

// int64Param is the number {key} in the request's path, or it answers 400
// saying what wasn't one.
func int64Param(w http.ResponseWriter, r *http.Request, key, what string) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, key), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad "+what+" id")
		return 0, false
	}
	return id, true
}

func auditEntry(u *auth.User, ip, instanceID, action, detail string) store.AuditEntry {
	e := store.AuditEntry{IP: ip, Instance: instanceID, Action: action, Detail: detail}
	if u != nil {
		e.UserID, e.Username = u.ID, u.Username
	}
	return e
}
