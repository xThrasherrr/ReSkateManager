// ReSkateManager runs ReSkate dedicated servers and serves a web panel to manage them.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
	// The Docker image has no zone files; with these, TZ still sets the clock
	// that scheduled restarts follow.
	_ "time/tzdata"

	"github.com/xThrasherrr/ReSkateManager/internal/adminsync"
	"github.com/xThrasherrr/ReSkateManager/internal/alerts"
	"github.com/xThrasherrr/ReSkateManager/internal/announce"
	"github.com/xThrasherrr/ReSkateManager/internal/api"
	"github.com/xThrasherrr/ReSkateManager/internal/auth"
	"github.com/xThrasherrr/ReSkateManager/internal/backup"
	"github.com/xThrasherrr/ReSkateManager/internal/config"
	"github.com/xThrasherrr/ReSkateManager/internal/housekeep"
	"github.com/xThrasherrr/ReSkateManager/internal/instance"
	"github.com/xThrasherrr/ReSkateManager/internal/lockfile"
	"github.com/xThrasherrr/ReSkateManager/internal/logfile"
	"github.com/xThrasherrr/ReSkateManager/internal/logparse"
	"github.com/xThrasherrr/ReSkateManager/internal/perf"
	"github.com/xThrasherrr/ReSkateManager/internal/restarts"
	"github.com/xThrasherrr/ReSkateManager/internal/serverconfig"
	"github.com/xThrasherrr/ReSkateManager/internal/store"
	"github.com/xThrasherrr/ReSkateManager/internal/thunderstore"
	"github.com/xThrasherrr/ReSkateManager/internal/updater"
)

var version = "dev"

func main() {
	root := flag.String("root", "", "folder for data/ and servers/ (default: next to the executable)")
	noTray := flag.Bool("no-tray", false, "run without the tray icon")
	noBrowser := flag.Bool("no-browser", false, "do not open the panel in a browser")
	launcherJSON := flag.String("launcher-json", "", "use this launcher.json (file or URL) instead of the latest release")
	showVersion := flag.Bool("version", false, "print the version and exit")
	restoreZip := flag.String("restore", "", "restore the database, settings and servers from this backup, then exit; stop the manager first")
	flag.Parse()
	if *showVersion {
		fmt.Println("ReSkateManager", version)
		return
	}
	if *restoreZip != "" {
		if err := restoreBackup(*root, *restoreZip); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	// Read once, now: after an update renames this binary to .old, Linux
	// answers os.Executable with the .old path, and the relaunch would start
	// the old manager again.
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		exe = ""
	}
	err = run(*root, *noTray, *noBrowser, *launcherJSON, exe)
	if errors.Is(err, errRestart) {
		if underSystemd() {
			fmt.Println("Exiting so systemd starts the updated manager.")
			os.Exit(3)
		}
		if err = relaunch(exe, relaunchArgs(os.Args[1:])); err == nil {
			return
		}
		err = fmt.Errorf("start the updated manager: %w", err)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func defaultRoot() string {
	if r := os.Getenv("RSM_ROOT"); r != "" {
		return r
	}
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

func run(root string, noTray, noBrowser bool, launcherJSON, exe string) error {
	if root == "" {
		root = defaultRoot()
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	dataDir := filepath.Join(root, "data")
	serversDir := filepath.Join(root, "servers")
	for _, d := range []string{dataDir, serversDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	// data/ holds the password hashes and sessions; keep other users on the
	// host out of it, in installs made before this too. (Windows goes by the
	// folder's ACL instead.)
	if err := os.Chmod(dataDir, 0o700); err != nil {
		return err
	}
	// One manager per folder: two would run each server twice and write one
	// database from both. Let go before an update relaunches the manager.
	lock, err := lockfile.Acquire(filepath.Join(dataDir, "manager.lock"))
	if errors.Is(err, lockfile.ErrLocked) {
		return fmt.Errorf("another manager is already running with %s; stop it first", root)
	}
	if err != nil {
		return err
	}
	defer lock.Release()

	logFile, err := logfile.Open(filepath.Join(dataDir, api.ManagerLog))
	if err != nil {
		return err
	}
	defer logFile.Close()
	log := slog.New(slog.NewTextHandler(io.MultiWriter(os.Stdout, logFile), &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)
	sweepLeftovers(log, root, exe)

	cfgPath := filepath.Join(dataDir, "manager.toml")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("manager.toml: %w", err)
	}
	for _, k := range cfg.Unknown {
		log.Warn("manager.toml has a setting this manager doesn't know, which does nothing; check its spelling", "key", k)
	}
	sharedDir := filepath.Join(root, serverconfig.SharedName)
	backups := &backup.Service{Dir: filepath.Join(root, "backups"), DataDir: dataDir, Root: root, SharedDir: sharedDir, Version: version, Log: log}
	db, err := store.OpenWith(filepath.Join(dataDir, "manager.db"), backups.BeforeMigration)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer db.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	authSvc, err := auth.New(ctx, db.DB)
	if err != nil {
		return err
	}
	reg := instance.NewRegistry(log)
	backups.Store, backups.Reg = db, reg
	admins := &adminsync.Syncer{Store: db, Auth: authSvc, Reg: reg, Log: log}

	builds := &updater.Builds{Store: db, Log: log}
	if err := builds.Load(ctx); err != nil {
		log.Warn("server builds learned before", "err", err)
	}
	checker := updater.NewChecker(cfg.UpdateRepo)
	checker.LauncherJSON = launcherJSON
	checker.Learn = builds.Learn
	githubAPI := os.Getenv("RSM_GITHUB_API") // a stand-in for api.github.com, for testing updates
	if githubAPI != "" {
		if err := updater.CheckOverride(githubAPI); err != nil {
			return fmt.Errorf("RSM_GITHUB_API: %w", err)
		}
	}
	if strings.Contains(launcherJSON, "://") { // an address, not a file
		if err := updater.CheckOverride(launcherJSON); err != nil {
			return fmt.Errorf("--launcher-json: %w", err)
		}
	}
	checker.API = githubAPI
	cacheDir := filepath.Join(root, "cache")
	updates := &updater.Service{Checker: checker, Builds: builds, Reg: reg, CacheDir: cacheDir, Log: log, Base: ctx}

	managerRepo := cfg.ManagerRepo
	if os.Getenv("RSM_NO_SELF_UPDATE") != "" {
		managerRepo = "" // the Docker image: a new manager comes from pulling a new image
	}
	self := &updater.SelfChecker{Repo: managerRepo, Current: version, API: githubAPI, Log: log}
	if exe != "" {
		go updater.RemoveOld(ctx, exe)
	}
	var restart restarter

	static, err := staticFS()
	if err != nil {
		return err
	}
	notifier := &alerts.Notifier{Store: db, Log: log, Version: version}
	notifier.Start(ctx)
	a := &api.API{Cfg: cfg, Store: db, Auth: authSvc, Reg: reg, Updates: updates, Builds: builds, Admins: admins, Self: self, Log: log, Version: version,
		ConfigPath: cfgPath, ServersDir: serversDir, SharedDir: sharedDir, DataDir: dataDir, CacheDir: cacheDir, Static: static, Backups: backups,
		Thunderstore: thunderstoreClient(), Alerts: notifier, Exe: exe, Restart: func() { restart.request(reg, stop) }, Base: ctx}
	serverName := func(id string) string {
		if in, ok := reg.Get(id); ok {
			return in.Def().Name
		}
		return id
	}
	updates.OnInstalled = func(id, ver, by string) {
		_ = db.Audit(context.Background(), store.AuditEntry{Instance: id, Action: "server.updated", Detail: ver, Username: by})
		notifier.Send(alerts.ForUpdate(serverName(id), a.PanelLink("/s/"+id), ver, by, nil))
	}
	updates.OnFailed = func(id, ver, by string, err error) {
		notifier.Send(alerts.ForUpdate(serverName(id), a.PanelLink("/s/"+id), ver, by, err))
	}
	updates.BeforeInstall = func(in *instance.Instance) error {
		info, err := backups.Create(context.Background(), backup.Options{Kind: backup.ServerUpdate, Servers: []string{in.ID()}})
		if err == nil {
			in.Note("Backed up the server's config first, in " + info.Name + ".")
		}
		return err
	}
	noteManagerVersion(ctx, db, notifier, a.PanelLink("/"), log)
	reg.Wire = func(in *instance.Instance) {
		id := in.ID()
		in.OnCrash = func(c instance.Crash) {
			notifier.Send(alerts.ForCrash(in.Def().Name, a.PanelLink("/s/"+id), c))
		}
		in.OnReady = func() {
			if err := admins.Sync(context.Background(), in); err != nil {
				log.Warn("in-game admin sync", "instance", id, "err", err)
			}
		}
		in.OnJoin = func(p instance.Player) {
			if err := db.SeenPlayer(context.Background(), id, p.ID, p.Name); err != nil {
				log.Warn("player history", "err", err)
			}
		}
		in.OnNetwork = func(run time.Time, n logparse.Network) {
			if err := db.AddNetSample(context.Background(), id, time.Now().Unix(), run.Unix(), n); err != nil {
				log.Warn("network sample", "instance", id, "err", err)
			}
		}
		in.LinkSharedMods = a.LinkSharedMods
	}
	defs, err := db.Instances(ctx)
	if err != nil {
		return err
	}
	for _, d := range defs {
		reg.Add(d)
	}
	a.RelinkSharedMods() // the manager's folder may have moved since its last run
	a.SweepLeftovers()
	srv := a.Server()
	// Cancelled when shutdown begins, so a request waiting on a server (a stop
	// or restart, up to 30 s) gives up at once instead of holding shutdown.
	reqs, cancelReqs := context.WithCancel(context.Background())
	defer cancelReqs()
	srv.BaseContext = func(net.Listener) context.Context { return reqs }
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("cannot listen on %s (is the manager already running?): %w", cfg.Listen, err)
	}
	tls := cfg.TLSCert != "" && cfg.TLSKey != ""
	go func() {
		var err error
		if tls {
			err = srv.ServeTLS(ln, cfg.TLSCert, cfg.TLSKey)
		} else {
			err = srv.Serve(ln)
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("panel server", "err", err)
			stop()
		}
	}()

	url := panelURL(cfg, tls)
	banner(ctx, url, cfg, authSvc)
	log.Info("ReSkateManager started", "version", version, "root", root, "listen", cfg.Listen)

	resume := takeResume(dataDir) // the servers that ran before an update restart
	for _, in := range reg.List() {
		if in.Def().AutoStart || slices.Contains(resume, in.ID()) {
			if err := in.Start(); err != nil {
				log.Warn("auto-start failed", "instance", in.ID(), "err", err)
			}
		}
	}
	var jobs sync.WaitGroup // background loops that use db, so they end before it closes
	jobs.Go(func() { updates.Run(ctx) })
	jobs.Go(func() { self.Run(ctx) })
	jobs.Go(func() { (&announce.Scheduler{Store: db, Reg: reg, Log: log}).Run(ctx) })
	jobs.Go(func() { (&perf.Recorder{Store: db, Reg: reg, Log: log}).Run(ctx) })
	jobs.Go(func() { (&housekeep.Keeper{Store: db, CacheDir: cacheDir, Log: log}).Run(ctx) })
	jobs.Go(func() { a.RunSweeps(ctx) })
	jobs.Go(func() {
		host := func() string { return a.PanelLink("/host") }
		(&alerts.Watcher{Store: db, Send: notifier.Send, Dirs: a.DiskDirs, Link: host, Log: log}).Run(ctx)
	})
	jobs.Go(func() {
		backups.Run(ctx, func(err error) { notifier.Send(alerts.ForBackupFailed(a.PanelLink("/manager"), err)) })
	})
	jobs.Go(func() {
		(&restarts.Scheduler{Reg: reg, Log: log, OnRestart: func(in *instance.Instance, due time.Time, err error) {
			e := store.AuditEntry{Instance: in.ID(), Action: "server.restart", Detail: "scheduled", Username: "schedule"}
			if err != nil {
				e.Detail = "scheduled; failed: " + err.Error()
				notifier.Send(alerts.ForRestartFailed(in.Def().Name, a.PanelLink("/s/"+in.ID()), err))
			}
			_ = db.Audit(context.Background(), e)
		}}).Run(ctx)
	})
	admins.Kick() // catch up on changes made while the manager was off

	if cfg.OpenBrowser && !noBrowser {
		openBrowser(setupURL(ctx, url, authSvc))
	}
	if !noTray {
		runTray(ctx, stop, url) // returns when ctx ends or Quit is chosen
	}
	<-ctx.Done()
	stop() // a second Ctrl+C now ends the manager instead of being swallowed

	// Close the panel before stopping servers, so no request can start one
	// after StopAll has taken its list. With their contexts cancelled, the
	// requests still running end quickly: a cancelled restart never starts.
	log.Info("shutting down; closing the panel")
	cancelReqs()
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		log.Warn("panel shutdown", "err", err)
	}
	// Backups, restores, imports and installs were told to stop with ctx; a
	// restore stopped part way puts back what it moved.
	if !a.Wait(30 * time.Second) {
		log.Warn("work the panel started was still running at shutdown")
	}
	if !updates.Wait(15 * time.Second) {
		log.Warn("a server update was still running at shutdown")
	}
	log.Info("stopping servers")
	reg.StopAll(25 * time.Second)
	jobs.Wait()
	if pending, ids := restart.requested(); pending {
		if err := writeResume(dataDir, ids); err != nil {
			log.Warn("remember running servers", "err", err)
		}
		log.Info("restarting into the updated manager")
		return errRestart
	}
	return nil
}

func panelURL(cfg config.Config, tls bool) string {
	if cfg.PublicURL != "" {
		return strings.TrimRight(cfg.PublicURL, "/")
	}
	host, port, _ := net.SplitHostPort(cfg.Listen)
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	scheme := "http"
	if tls {
		scheme = "https"
	}
	return scheme + "://" + net.JoinHostPort(host, port)
}

func banner(ctx context.Context, url string, cfg config.Config, a *auth.Service) {
	fmt.Println()
	fmt.Println("  ReSkateManager " + version)
	fmt.Println("  Panel: " + url)
	if strings.HasPrefix(cfg.Listen, "0.0.0.0") || strings.HasPrefix(cfg.Listen, ":") {
		for _, ip := range lanIPs() {
			_, port, _ := net.SplitHostPort(cfg.Listen)
			fmt.Println("         http://" + net.JoinHostPort(ip, port))
		}
	}
	if required, err := a.SetupRequired(ctx); err == nil && required {
		fmt.Println()
		fmt.Println("  First run: open the panel and enter this setup PIN to create the owner account:")
		fmt.Println()
		fmt.Println("      " + a.SetupPIN())
	}
	fmt.Println()
}

// setupURL is where the browser opens on first run: the setup page with the
// PIN filled in. It rides after the #, which the browser never sends, so it
// stays out of every log and proxy; the PIN dies with the setup anyway.
func setupURL(ctx context.Context, url string, a *auth.Service) string {
	if required, err := a.SetupRequired(ctx); err != nil || !required {
		return url
	}
	return url + "/setup#pin=" + a.SetupPIN()
}

func lanIPs() []string {
	var out []string
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && !ipn.IP.IsLoopback() && !ipn.IP.IsLinkLocalUnicast() {
			out = append(out, ipn.IP.String())
		}
	}
	return out
}

// thunderstoreClient looks up ReSkate mods on Thunderstore. Its requests name
// the manager, its version and where the project lives, so Thunderstore can
// tell whom to ask about them.
func thunderstoreClient() *thunderstore.Client {
	ts := thunderstore.New("reskate")
	ts.UserAgent = "ReSkateManager/" + version + " (+https://github.com/xThrasherrr/ReSkateManager)"
	return ts
}

// noteManagerVersion alerts when the manager starts on a version it had not
// run before: an update from the panel, a newer Docker image, or one put in
// place by hand.
func noteManagerVersion(ctx context.Context, db *store.Store, n *alerts.Notifier, link string, log *slog.Logger) {
	const key = "manager.version"
	prev, err := db.Get(ctx, key)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		log.Warn("last manager version", "err", err)
		return
	}
	if prev == version {
		return
	}
	if prev != "" {
		n.Send(alerts.ForManagerUpdate(prev, version, link))
	}
	if err := db.Set(ctx, key, version); err != nil {
		log.Warn("remember manager version", "err", err)
	}
}

// sweepLeftovers deletes what the manager left when it stopped part way
// through a backup, a settings write or its own update, and logs what it
// leaves for a person: a restore's set-aside mods are the only copy.
func sweepLeftovers(log *slog.Logger, root, exe string) {
	remove := func(pattern string) {
		matches, _ := filepath.Glob(pattern)
		for _, p := range matches {
			if err := os.RemoveAll(p); err == nil {
				log.Info("removed a leftover from an earlier run", "path", p)
			}
		}
	}
	remove(filepath.Join(root, "backups", ".partial-*.zip"))
	remove(filepath.Join(root, "backups", ".manager-*.db"))
	remove(filepath.Join(root, "data", ".manager-*.toml"))
	remove(filepath.Join(root, "data", "manager.db.restore*"))
	remove(filepath.Join(root, "servers", ".importing-*"))
	if exe != "" {
		remove(exe + ".new")
		remove(filepath.Join(filepath.Dir(exe), ".update-*"))
	}
	restores, _ := filepath.Glob(filepath.Join(root, "servers", "*", ".restore-*"))
	shared, _ := filepath.Glob(filepath.Join(root, serverconfig.SharedName, ".restore-*"))
	for _, p := range append(restores, shared...) {
		log.Warn("a restore was cut short and set mods aside here; move back what you want to keep, then delete it", "path", p)
	}
}
