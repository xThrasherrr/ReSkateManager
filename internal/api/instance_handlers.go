package api

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/xThrasherrr/ReSkateManager/internal/auth"
	"github.com/xThrasherrr/ReSkateManager/internal/instance"
	"github.com/xThrasherrr/ReSkateManager/internal/perf"
	"github.com/xThrasherrr/ReSkateManager/internal/serverconfig"
	"github.com/xThrasherrr/ReSkateManager/internal/store"
)

type instanceView struct {
	instance.View
	Permissions []string `json:"permissions"`
	// ServerVersion is the release the server's program is from; empty when
	// it isn't installed or isn't a release the manager knows.
	ServerVersion string `json:"serverVersion,omitempty"`
}

func (a *API) viewFor(r *http.Request, in *instance.Instance) instanceView {
	v := in.View()
	return instanceView{View: v, Permissions: from(r).perms.For(in.ID()), ServerVersion: a.Builds.Version(v.Def.Exe())}
}

func (a *API) listInstances(w http.ResponseWriter, r *http.Request) {
	c := from(r)
	out := []instanceView{}
	for _, in := range a.Reg.List() {
		if c.perms.Sees(in.ID()) {
			out = append(out, a.viewFor(r, in))
		}
	}
	writeJSON(w, 200, out)
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(name string) string {
	s := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(s) > 32 {
		s = strings.Trim(s[:32], "-")
	}
	if s == "" {
		s = "server"
	}
	return s
}

// usedPorts collects every port the instances' configs claim.
func (a *API) usedPorts() map[int]bool {
	used := map[int]bool{}
	for _, in := range a.Reg.List() {
		f, err := serverconfig.Read(in.Def().ConfigPath())
		if err != nil {
			continue
		}
		v := f.Values()
		for _, k := range []string{"port", "query_port"} {
			if n, ok := v[k].(float64); ok {
				used[int(n)] = true
			}
		}
	}
	return used
}

func udpFree(port int) bool {
	c, err := net.ListenPacket("udp", ":"+strconv.Itoa(port))
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// freePorts finds a game and query port pair no server uses and nothing on
// the machine holds, from 27015 up; ok is false when 200 pairs are all taken.
func (a *API) freePorts() (port, query int, ok bool) {
	used := a.usedPorts()
	for p := 27015; p < 27015+400; p += 2 {
		if !used[p] && !used[p+1] && udpFree(p) && udpFree(p+1) {
			return p, p + 1, true
		}
	}
	return 0, 0, false
}

// serverName checks a name for a server in the panel: 1 to 64 characters,
// with no control or bidirectional-override characters, which could make one
// server's name look like another's.
func serverName(s string) (string, error) {
	s = strings.TrimSpace(s)
	if n := utf8.RuneCountInString(s); n == 0 || n > 64 || !utf8.ValidString(s) {
		return "", errors.New("names are 1-64 characters")
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			return "", fmt.Errorf("a name can't hold the character %U", r)
		}
	}
	return s, nil
}

func (a *API) createInstance(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		Dir     string `json:"dir"`
		Install bool   `json:"install"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	name, err := serverName(req.Name)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	req.Name = name
	a.instMu.Lock()
	defer a.instMu.Unlock()
	id := slugify(req.Name)
	for i := 2; ; i++ {
		if _, taken := a.Reg.Get(id); !taken && !a.reserved[id] {
			break
		}
		id = fmt.Sprintf("%s-%d", slugify(req.Name), i)
	}
	dir := strings.TrimSpace(req.Dir)
	if dir == "" {
		dir = filepath.Join(a.ServersDir, id)
	} else if !filepath.IsAbs(dir) {
		writeErr(w, 400, "the folder must be a full path")
		return
	}
	dir = filepath.Clean(dir)
	resolved := realPath(dir)
	// The manager writes into a server's folder, runs the program it finds
	// there and may delete it, so a folder anywhere on the host is for owners.
	// Inside the servers folder means as written and where symlinks lead.
	if !from(r).perms.Owner {
		sd := filepath.Clean(a.ServersDir)
		realSD := realPath(sd)
		if a.ServersDir == "" || dir == sd || resolved == realSD || !lexWithin(dir, sd) || !lexWithin(resolved, realSD) {
			writeErr(w, 403, "only owners can add a server outside the servers folder")
			return
		}
	}
	// Whoever may change the shared mods could otherwise put a program there.
	if a.SharedDir != "" && within(dir, a.SharedDir) {
		writeErr(w, 400, "that folder is inside the shared mods folder")
		return
	}
	for _, other := range a.Reg.List() {
		od := filepath.Clean(other.Def().Dir)
		switch {
		case samePath(od, dir) || samePath(realPath(od), resolved):
			writeErr(w, 400, "another server already uses that folder")
			return
		// Whoever may upload mods to one server could put a program in the
		// other's folder, which the manager would then run.
		case inMods(dir, od):
			writeErr(w, 400, fmt.Sprintf("that folder is inside %s's Mods folder", other.Def().Name))
			return
		case inMods(od, dir):
			writeErr(w, 400, fmt.Sprintf("that folder's Mods folder holds %s's folder", other.Def().Name))
			return
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		a.fail(w, r, 400, fmt.Errorf("cannot create the folder: %w", err))
		return
	}
	// A fresh folder gets ports no other instance uses; the server fills in the
	// rest of its settings with defaults on first start. The panel name may
	// have characters the server refuses in its own name.
	cfgPath := filepath.Join(dir, "ReSkateServer.json")
	if _, err := os.Stat(cfgPath); errors.Is(err, os.ErrNotExist) {
		port, query, ok := a.freePorts()
		if !ok {
			writeErr(w, http.StatusConflict, "every port pair from 27015 to 27414 is taken; free one, or add the server with its own ReSkateServer.json")
			return
		}
		f := &serverconfig.File{Root: map[string]any{"name": serverconfig.ServerName(req.Name), "port": port, "query_port": query, "auto_update": false}}
		if err := f.Write(cfgPath); err != nil {
			a.internalErr(w, r, err)
			return
		}
	}
	d := instance.Def{ID: id, Name: req.Name, Dir: dir, AutoRestart: true}
	if err := a.Store.CreateInstance(r.Context(), d); err != nil {
		a.fail(w, r, 400, err)
		return
	}
	in := a.Reg.Add(d)
	a.audit(r, id, "instance.create", dir)
	a.Admins.Kick()
	if req.Install && !in.View().Installed {
		if err := a.Updates.Start(in, false, from(r).user.Username); err != nil {
			in.Note("Could not install the server: " + err.Error())
		}
	}
	writeJSON(w, 201, a.viewFor(r, in))
}

func (a *API) updateInstance(w http.ResponseWriter, r *http.Request) {
	in := inst(r)
	var req struct {
		Name         *string   `json:"name"`
		AutoStart    *bool     `json:"autoStart"`
		AutoRestart  *bool     `json:"autoRestart"`
		AutoUpdate   *bool     `json:"autoUpdate"`
		RestartTimes *[]string `json:"restartTimes"`
		RestartHours *int      `json:"restartHours"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	a.instMu.Lock()
	defer a.instMu.Unlock()
	d := in.Def()
	if req.RestartTimes != nil {
		times, err := instance.ParseRestartTimes(*req.RestartTimes)
		if err != nil {
			a.fail(w, r, 400, err)
			return
		}
		d.RestartTimes = times
	}
	if req.RestartHours != nil {
		if *req.RestartHours < 0 || *req.RestartHours > instance.MaxRestartHours {
			writeErr(w, 400, fmt.Sprintf("restart after 1 to %d hours, or 0 for never", instance.MaxRestartHours))
			return
		}
		d.RestartHours = *req.RestartHours
	}
	if req.Name != nil {
		n, err := serverName(*req.Name)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		d.Name = n
	}
	if req.AutoStart != nil {
		d.AutoStart = *req.AutoStart
	}
	if req.AutoRestart != nil {
		d.AutoRestart = *req.AutoRestart
	}
	if req.AutoUpdate != nil {
		d.AutoUpdate = *req.AutoUpdate
	}
	if err := a.Store.UpdateInstance(r.Context(), d); err != nil {
		a.fail(w, r, errStatus(err), err)
		return
	}
	in.SetDef(d)
	a.audit(r, d.ID, "instance.update", fmt.Sprintf("autoStart=%v autoRestart=%v autoUpdate=%v restartTimes=%s restartHours=%d",
		d.AutoStart, d.AutoRestart, d.AutoUpdate, strings.Join(d.RestartTimes, ","), d.RestartHours))
	writeJSON(w, 200, a.viewFor(r, in))
}

// within reports whether path is dir or inside it, as written or where
// symlinks lead: a link can put a folder that looks separate inside another.
func within(path, dir string) bool {
	return lexWithin(path, dir) || lexWithin(realPath(path), realPath(dir))
}

// samePath reports whether a and b name one folder: on Windows, whatever
// their case; elsewhere, where Mods and mods are two folders, spelled alike.
func samePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// lexWithin reports whether path, as written, is dir or inside it.
func lexWithin(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// realPath resolves the symlinks in the part of p that exists. The rest is
// joined on as written: it can't link anywhere until it's made.
func realPath(p string) string {
	p = filepath.Clean(p)
	rest := ""
	for {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return filepath.Join(p, rest)
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}

// inMods reports whether path is server folder dir's Mods or DisabledMods
// folder, or inside one.
func inMods(path, dir string) bool {
	return within(path, serverconfig.ModsDir(dir)) || within(path, serverconfig.DisabledModsDir(dir))
}

// purgeable refuses to delete a folder that holds more than this server: a
// drive root, the manager's own folders, or another server's folder.
func (a *API) purgeable(in *instance.Instance) error {
	dir := filepath.Clean(in.Def().Dir)
	if resolved := realPath(dir); !filepath.IsAbs(dir) || filepath.Dir(dir) == dir || filepath.Dir(resolved) == resolved {
		return errors.New("the server's folder is a drive root; delete its files by hand")
	}
	own := []string{a.ServersDir, a.SharedDir}
	if a.ConfigPath != "" {
		own = append(own, filepath.Dir(a.ConfigPath)) // data/, with the database
	}
	if a.Exe != "" {
		own = append(own, filepath.Dir(a.Exe))
	}
	for _, d := range own {
		if d != "" && within(d, dir) {
			return errors.New("the server's folder holds the manager's own files; delete its files by hand")
		}
	}
	for _, other := range a.Reg.List() {
		if other.ID() != in.ID() && (within(other.Def().Dir, dir) || within(dir, other.Def().Dir)) {
			return fmt.Errorf("the server's folder overlaps %s's; delete its files by hand", other.Def().Name)
		}
	}
	return nil
}

var errFiles = errors.New("could not delete the server's files")

func (a *API) deleteInstance(w http.ResponseWriter, r *http.Request) {
	in := inst(r)
	dir := in.Def().Dir
	purge := r.URL.Query().Get("purge") == "1"
	if purge {
		if err := a.purgeable(in); err != nil {
			a.fail(w, r, 400, err)
			return
		}
	}
	// Held as if updating, so nothing starts it, while its files go: that
	// can take minutes, and the instance's lock would hold up everyone
	// listing servers meanwhile.
	if err := in.BeginUpdate(); err != nil {
		writeErr(w, 409, "stop the server, and let any update or restore finish, before removing it")
		return
	}
	// Not cancelled with the request: once files are going, the rest must follow.
	ctx := context.WithoutCancel(r.Context())
	err := func() error {
		// Files go first, so a server whose files could not all be deleted
		// stays in the panel for another try.
		if purge {
			// RemoveAll never follows a link, but drop the ones to the shared
			// mods first all the same.
			if err := serverconfig.SyncShared(dir, a.SharedDir, serverconfig.SharedOff); err != nil {
				return fmt.Errorf("%w: %w", errFiles, err)
			}
			if err := os.RemoveAll(dir); err != nil {
				return fmt.Errorf("%w: %w", errFiles, err)
			}
		}
		return a.Store.DeleteInstance(ctx, in.ID())
	}()
	if err != nil {
		in.EndUpdate()
	}
	if errors.Is(err, errFiles) {
		a.Log.Error("delete server files", "instance", in.ID(), "err", err)
		writeErr(w, 500, "could not delete all of the server's files, so it stays in the panel to try again; the manager log has the details")
		return
	}
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	a.Reg.Remove(in.ID())
	if purge {
		a.audit(r, in.ID(), "instance.purge", dir)
		writeJSON(w, 200, map[string]any{"ok": true, "filesDeleted": dir})
		return
	}
	a.audit(r, in.ID(), "instance.delete", dir)
	writeJSON(w, 200, map[string]any{"ok": true, "filesKept": dir})
}

func (a *API) lifecycle(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		in := inst(r)
		// How long a stop may take before the request gives up on it; the
		// server is killed after 15 s of not quitting anyway.
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		var err error
		switch action {
		case "start":
			err = in.Start()
		case "stop":
			err = in.Stop(ctx)
		case "restart":
			err = in.Restart(ctx)
		case "kill":
			in.Kill()
		}
		a.auditOutcome(r, in.ID(), "server."+action, "", err)
		if err != nil {
			a.fail(w, r, errStatus(err), err)
			return
		}
		writeJSON(w, 200, a.viewFor(r, in))
	}
}

func (a *API) command(w http.ResponseWriter, r *http.Request) {
	in := inst(r)
	var req struct {
		Line string `json:"line"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	line := strings.TrimSpace(req.Line)
	if line == "" {
		writeErr(w, 400, "empty command")
		return
	}
	if len(line) > maxCommand {
		writeErr(w, 400, fmt.Sprintf("a command can be at most %d bytes", maxCommand))
		return
	}
	c := from(r)
	if p := consolePerm(strings.Fields(line)[0]); p != "" && !c.perms.Can(p, in.ID()) {
		writeErr(w, 403, "that command needs a permission you don't have: "+permLabel(p))
		return
	}
	reply, err := in.Command(r.Context(), line, c.user.Username)
	a.auditOutcome(r, in.ID(), "console.exec", auditLine(line), err)
	a.answerReply(w, r, reply, err)
}

// maxCommand is the longest console line the panel sends. The server's own
// longest, a list of score-allow fingerprints, is well under it.
const maxCommand = 1024

// consoleOnly are the console commands console.exec alone may run: they only
// read, or act on players in a way no finer permission covers.
var consoleOnly = []string{"help", "status", "net", "players", "bans", "maps", "tpall", "tphere", "vote-cancel", "clear-objects"}

// consolePerms are the console commands that do what a finer permission
// grants, and need it too. The verbs are the server's own (Host::command in
// Server/server_host.cpp), aliases included.
var consolePerms = func() map[string]string {
	m := map[string]string{"admin": auth.IngameAdmins, "admins": auth.IngameAdmins, "ban": auth.PlayersBan, "unban": auth.PlayersBan,
		"kick": auth.PlayersKick, "say": auth.PlayersChat, "msg": auth.PlayersChat, "msg-party": auth.PlayersChat, "msg-admins": auth.PlayersChat}
	for _, verb := range serverconfig.SettingVerbs {
		m[verb] = auth.SettingsEdit
	}
	return m
}()

// consolePerm is the permission a console command needs besides console.exec,
// "" for none. A command the manager doesn't know needs settings.edit, so one
// a newer server adds can't change settings for someone who may not.
func consolePerm(verb string) string {
	verb = strings.ToLower(verb)
	if slices.Contains(consoleOnly, verb) {
		return ""
	}
	if p, ok := consolePerms[verb]; ok {
		return p
	}
	return auth.SettingsEdit
}

// permLabel names a permission as the Users page does.
func permLabel(key string) string {
	for _, p := range auth.AllPerms {
		if p.Key == key {
			return p.Label
		}
	}
	return key
}

// auditLine keeps a password out of the audit log.
func auditLine(line string) string {
	if verb, arg, ok := strings.Cut(line, " "); ok && strings.EqualFold(verb, "password") && !strings.EqualFold(strings.TrimSpace(arg), "off") {
		return "password <hidden>"
	}
	return line
}

// ---- players ----

func (a *API) players(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, inst(r).Players())
}

func (a *API) history(w http.ResponseWriter, r *http.Request) {
	rows, err := a.Store.PlayerHistory(r.Context(), inst(r).ID(), strings.TrimSpace(r.URL.Query().Get("q")), 200)
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	if rows == nil {
		rows = []store.PlayerRecord{}
	}
	writeJSON(w, 200, rows)
}

func steamParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "steamId")
	if !serverconfig.SteamIDRe.MatchString(id) {
		writeErr(w, 400, "not a SteamID64")
		return "", false
	}
	return id, true
}

func running(in *instance.Instance) bool {
	s := in.State()
	return s == instance.Running || s == instance.Starting
}

func (a *API) kick(w http.ResponseWriter, r *http.Request) {
	in := inst(r)
	id, ok := steamParam(w, r)
	if !ok {
		return
	}
	reply, err := in.Command(r.Context(), "kick "+id, from(r).user.Username)
	a.auditOutcome(r, in.ID(), "player.kick", id, err)
	a.answerReply(w, r, reply, err)
}

func (a *API) bans(w http.ResponseWriter, r *http.Request) {
	f, err := serverconfig.Read(inst(r).Def().ConfigPath())
	if err != nil {
		writeErr(w, 404, "the server has no config yet; start it once")
		return
	}
	writeJSON(w, 200, f.Bans())
}

// cleanText makes text someone typed fit on a console line, as the server's
// clean_chat_text does with chat: line breaks and tabs become spaces, other
// control characters and bytes that aren't UTF-8 go, and the ends are trimmed.
func cleanText(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			return ' '
		case r < 0x20 || r == 0x7f || r >= 0x80 && r <= 0x9f:
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, ""))
	return strings.TrimSpace(s)
}

// cut shortens s to at most n bytes, on a character boundary.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func (a *API) ban(w http.ResponseWriter, r *http.Request) {
	in := inst(r)
	var req struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if !serverconfig.SteamIDRe.MatchString(req.ID) {
		writeErr(w, 400, "not a SteamID64")
		return
	}
	name := strings.TrimSpace(cut(cleanText(req.Name), 64))
	var reply string
	var err error
	if running(in) {
		reply, err = in.Command(r.Context(), strings.TrimSpace("ban "+req.ID+" "+name), from(r).user.Username)
	} else {
		reply, err = a.offlineEdit(in, func(f *serverconfig.File) (string, error) {
			bans := f.Bans()
			if slices.ContainsFunc(bans, func(b serverconfig.Ban) bool { return b.ID == req.ID }) {
				return "", errors.New(req.ID + " is already banned")
			}
			f.SetBans(append(bans, serverconfig.Ban{ID: req.ID, Name: name, Added: time.Now().Unix()}))
			return req.ID + " was banned.", nil
		})
	}
	a.auditOutcome(r, in.ID(), "player.ban", strings.TrimSpace(req.ID+" "+name), err)
	a.answerReply(w, r, reply, err)
}

func (a *API) unban(w http.ResponseWriter, r *http.Request) {
	in := inst(r)
	id, ok := steamParam(w, r)
	if !ok {
		return
	}
	var reply string
	var err error
	if running(in) {
		reply, err = in.Command(r.Context(), "unban "+id, from(r).user.Username)
	} else {
		reply, err = a.offlineEdit(in, func(f *serverconfig.File) (string, error) {
			bans := f.Bans()
			n := len(bans)
			bans = slices.DeleteFunc(bans, func(b serverconfig.Ban) bool { return b.ID == id })
			if len(bans) == n {
				return "", errors.New("that SteamID64 is not banned")
			}
			f.SetBans(bans)
			return id + " was unbanned.", nil
		})
	}
	a.auditOutcome(r, in.ID(), "player.unban", id, err)
	a.answerReply(w, r, reply, err)
}

// offlineEdit changes ReSkateServer.json while the server is stopped.
func (a *API) offlineEdit(in *instance.Instance, fn func(*serverconfig.File) (string, error)) (string, error) {
	var reply string
	path := in.Def().ConfigPath() // outside IfStopped: it holds the instance lock
	ran, err := in.IfStopped(func() error {
		f, err := serverconfig.Read(path)
		if err != nil {
			return err
		}
		if reply, err = fn(f); err != nil {
			return err
		}
		return f.Write(path)
	})
	if !ran {
		return "", errJustStarted
	}
	return reply, err
}

var errJustStarted = errors.New("the server just started; try again")

// answerReply answers a change made by a console command or to the config
// file: the server's reply, or why there is none.
func (a *API) answerReply(w http.ResponseWriter, r *http.Request, reply string, err error) {
	switch {
	case errors.Is(err, errJustStarted):
		writeErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, instance.ErrNoReply):
		writeErr(w, http.StatusGatewayTimeout, err.Error())
	case err != nil:
		a.fail(w, r, 400, err)
	default:
		writeJSON(w, 200, map[string]string{"reply": reply})
	}
}

// auditOutcome records what the caller did, and if it failed, why.
func (a *API) auditOutcome(r *http.Request, instanceID, action, detail string, err error) {
	if err != nil {
		detail = strings.TrimSpace(detail + " (failed: " + err.Error() + ")")
	}
	a.audit(r, instanceID, action, detail)
}

func (a *API) say(w http.ResponseWriter, r *http.Request) {
	in := inst(r)
	var req struct {
		Message string `json:"message"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	msg := cleanText(req.Message)
	if msg == "" || len(msg) > 200 {
		writeErr(w, 400, "messages are 1-200 bytes")
		return
	}
	reply, err := in.Command(r.Context(), "say "+msg, from(r).user.Username)
	a.auditOutcome(r, in.ID(), "player.say", msg, err)
	a.answerReply(w, r, reply, err)
}

// ---- settings ----

func (a *API) settings(w http.ResponseWriter, r *http.Request) {
	in := inst(r)
	f, err := serverconfig.Read(in.Def().ConfigPath())
	if err != nil {
		writeErr(w, 404, "the server has no config yet; start it once")
		return
	}
	// Settings a server laid out in sections no longer has.
	gone := []string{}
	if f.Sectioned() {
		gone = serverconfig.Removed
	}
	// A running server has written every setting it knows into the file (it
	// adds missing ones at start), so a key still missing is newer than it.
	unknown := []string{}
	if in.State() == instance.Running {
		for _, fd := range serverconfig.Fields {
			if _, ok := f.Get(fd.Key); !ok && !slices.Contains(gone, fd.Key) {
				unknown = append(unknown, fd.Key)
			}
		}
	}
	values := f.Values()
	if !from(r).perms.Owner {
		maps.DeleteFunc(values, func(k string, _ any) bool { return serverconfig.OwnerOnly(k) })
	}
	writeJSON(w, 200, map[string]any{"values": values, "running": running(in), "unknown": unknown, "gone": gone})
}

type cmdResult struct {
	Command string `json:"command"`
	Reply   string `json:"reply"`
	Error   string `json:"error,omitempty"`
}

func (a *API) saveSettings(w http.ResponseWriter, r *http.Request) {
	in := inst(r)
	var req struct {
		Values  map[string]any `json:"values"`
		Restart bool           `json:"restart"` // allow a restart for settings that need one
	}
	if !readJSON(w, r, &req) {
		return
	}
	if !from(r).perms.Owner {
		for k := range req.Values {
			if serverconfig.OwnerOnly(k) {
				writeErr(w, http.StatusForbidden, "only owners can change "+k)
				return
			}
		}
	}
	path := in.Def().ConfigPath()
	f, err := serverconfig.Read(path)
	if err != nil {
		writeErr(w, 404, "the server has no config yet; start it once")
		return
	}
	// The map pool is checked against the maps the server has, or will have
	// once it starts.
	var known []string
	if _, ok := req.Values["map_pool"]; ok {
		if running(in) {
			if known, err = in.Maps(r.Context()); err != nil {
				a.fail(w, r, 400, fmt.Errorf("could not list the server's maps: %w", err))
				return
			}
		} else if known, err = serverconfig.KnownMaps(in.Def().Dir); err != nil {
			a.internalErr(w, r, err)
			return
		}
	}
	plan, err := serverconfig.Diff(f, req.Values, known)
	if err != nil {
		a.fail(w, r, 400, err)
		return
	}
	keys := make([]string, 0, len(plan.Changed))
	for k := range plan.Changed {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if len(keys) == 0 {
		writeJSON(w, 200, map[string]any{"changed": keys, "results": []cmdResult{}})
		return
	}
	user := from(r).user.Username

	if !running(in) {
		reply, err := a.offlineEdit(in, func(f *serverconfig.File) (string, error) {
			f.ApplyOffline(plan.Changed)
			return fmt.Sprintf("Saved %d setting(s); they apply when the server starts.", len(keys)), nil
		})
		a.auditOutcome(r, in.ID(), "settings.save", strings.Join(keys, ", "), err)
		a.answerReply(w, r, reply, err)
		return
	}
	if len(plan.Restart) > 0 && !req.Restart {
		restartKeys := make([]string, 0, len(plan.Restart))
		for k := range plan.Restart {
			restartKeys = append(restartKeys, k)
		}
		writeJSON(w, 409, map[string]any{"error": "some settings need a restart", "restartKeys": restartKeys})
		return
	}
	results := []cmdResult{}
	failed := 0
	for _, cmd := range plan.Commands {
		reply, err := in.Command(r.Context(), cmd, user)
		res := cmdResult{Command: auditLine(cmd), Reply: reply}
		if err != nil {
			res.Error = err.Error()
			failed++
		}
		results = append(results, res)
	}
	detail := strings.Join(keys, ", ")
	if failed > 0 {
		detail += fmt.Sprintf(" (%d of %d commands failed)", failed, len(plan.Commands))
	}
	a.audit(r, in.ID(), "settings.save", detail)
	restarted := false
	if len(plan.Restart) > 0 {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		err := in.Stop(ctx)
		cancel()
		if err != nil {
			a.fail(w, r, 500, fmt.Errorf("could not stop the server: %w", err))
			return
		}
		ran, err := in.IfStopped(func() error {
			f, err := serverconfig.Read(path)
			if err != nil {
				return err
			}
			f.ApplyOffline(plan.Restart)
			return f.Write(path)
		})
		if !ran {
			writeErr(w, http.StatusConflict, "the server was started by someone else before the settings were written")
			return
		}
		if err != nil {
			a.internalErr(w, r, err)
			return
		}
		// Start's errors are the ones the start button shows too.
		if err := in.Start(); err != nil {
			a.fail(w, r, errStatus(err), err)
			return
		}
		restarted = true
	}
	// The server saved what it accepted; report keys that did not take.
	notApplied := []string{}
	if after, err := serverconfig.Read(path); err == nil {
		vals := after.Values()
		for k, v := range plan.Changed {
			if _, isRestart := plan.Restart[k]; isRestart {
				continue
			}
			if fmt.Sprint(vals[k]) != fmt.Sprint(v) {
				notApplied = append(notApplied, k)
			}
		}
	}
	writeJSON(w, 200, map[string]any{"changed": keys, "results": results, "restarted": restarted, "notApplied": notApplied})
}

// ---- in-game admins ----

type adminRow struct {
	ID    string `json:"id"`
	Name  string `json:"name,omitempty"`
	Panel string `json:"panel,omitempty"` // the panel user this admin is kept for
}

func (a *API) admins(w http.ResponseWriter, r *http.Request) {
	in := inst(r)
	f, err := serverconfig.Read(in.Def().ConfigPath())
	if err != nil {
		writeErr(w, 404, "the server has no config yet; start it once")
		return
	}
	names := map[string]string{}
	if hist, err := a.Store.PlayerHistory(r.Context(), in.ID(), "", 5000); err == nil {
		for _, h := range hist {
			names[h.SteamID] = h.Name
		}
	}
	for _, p := range in.Players() {
		names[p.ID] = p.Name
	}
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

func (a *API) addAdmin(w http.ResponseWriter, r *http.Request) {
	in := inst(r)
	var req struct {
		ID string `json:"id"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if !serverconfig.SteamIDRe.MatchString(req.ID) {
		writeErr(w, 400, "not a SteamID64")
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
	panel, err := a.Admins.PanelAdmins(r.Context(), in.ID())
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	if user := panel[id]; user != "" {
		writeErr(w, 409, "this admin comes from panel user "+user+"; change their roles or Steam link instead")
		return
	}
	a.adminChange(w, r, in, "remove", id)
}

func (a *API) adminChange(w http.ResponseWriter, r *http.Request, in *instance.Instance, op, id string) {
	var reply string
	var err error
	if running(in) {
		reply, err = in.Command(r.Context(), "admin "+op+" "+id, from(r).user.Username)
	} else {
		reply, err = a.offlineEdit(in, func(f *serverconfig.File) (string, error) {
			list := f.Admins()
			if op == "add" {
				if !slices.Contains(list, id) {
					list = append(list, id)
				}
				f.SetAdmins(list)
				return id + " is an admin.", nil
			}
			f.SetAdmins(slices.DeleteFunc(list, func(x string) bool { return x == id }))
			return id + " is no longer an admin.", nil
		})
	}
	a.auditOutcome(r, in.ID(), "admin."+op, id, err)
	a.answerReply(w, r, reply, err)
}

// ---- updates ----

func (a *API) updateStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, a.Updates.Status(r.Context(), inst(r), r.URL.Query().Get("refresh") == "1"))
}

func (a *API) startUpdate(w http.ResponseWriter, r *http.Request) {
	in := inst(r)
	var req struct {
		Mode string `json:"mode"` // now | empty
	}
	if !readJSON(w, r, &req) {
		return
	}
	if err := a.Updates.Start(in, req.Mode == "empty", from(r).user.Username); err != nil {
		a.fail(w, r, errStatus(err), err)
		return
	}
	a.audit(r, in.ID(), "server.update", req.Mode)
	writeJSON(w, 202, a.Updates.Status(r.Context(), in, false))
}

// mods lists the server's Mods folder and the maps it adds, for the Mods page
// and the settings page's map choice. The server reads Mods only when it
// starts, so a running one also reports the maps it has loaded: a map added
// since needs a restart, and one whose mod was removed is still playable.
func (a *API) mods(w http.ResponseWriter, r *http.Request) {
	in := inst(r)
	dir := in.Def().Dir
	mods, err := serverconfig.ReadMods(dir)
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	var loaded []string
	if in.State() == instance.Running {
		if loaded, err = in.Maps(r.Context()); err != nil {
			loaded = nil
		}
	}
	// The map setting and the map pool, so disabling a mod they need can warn.
	var current any
	pool := []string{}
	if f, err := serverconfig.Read(in.Def().ConfigPath()); err == nil {
		current, _ = f.Get("map")
		if p, ok := f.Values()["map_pool"].([]string); ok {
			pool = p
		}
	}
	// The server's own copies that keep a shared mod of the same folder out.
	var shared map[string]any
	if a.SharedDir != "" {
		ownCopy := []string{}
		if in.Def().SharedMods != serverconfig.SharedOff {
			folders := a.sharedFolders()
			for _, m := range mods {
				if !m.Shared && folders[m.Folder] {
					ownCopy = append(ownCopy, m.Folder)
				}
			}
		}
		shared = map[string]any{"use": in.Def().SharedMods, "own": ownCopy}
	}
	writeJSON(w, 200, map[string]any{
		"folder": serverconfig.ModsDir(dir),
		"retail": serverconfig.RetailMaps,
		"mods":   mods,
		"loaded": loaded,
		"map":    current,
		"pool":   pool,
		"shared": shared, // null with the shared mods turned off
	})
}

// perfRanges are the windows the performance page offers, each averaged into
// buckets so a chart gets at most a few hundred points.
var perfRanges = map[string]struct{ span, bucket time.Duration }{
	"1h":  {time.Hour, perf.Interval},
	"6h":  {6 * time.Hour, 2 * time.Minute},
	"24h": {24 * time.Hour, 5 * time.Minute},
	"7d":  {7 * 24 * time.Hour, 30 * time.Minute},
}

func (a *API) performance(w http.ResponseWriter, r *http.Request) {
	rg, ok := perfRanges[r.URL.Query().Get("range")]
	if !ok {
		rg = perfRanges["1h"]
	}
	since := time.Now().Add(-rg.span).Unix()
	bucket := int64(rg.bucket / time.Second)
	points, err := a.Store.PerfPoints(r.Context(), inst(r).ID(), since, bucket)
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	runs, err := a.Store.PerfRuns(r.Context(), inst(r).ID(), since, 50)
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	latest, err := a.Store.LatestPerf(r.Context(), inst(r).ID())
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	network, err := a.Store.NetPoints(r.Context(), inst(r).ID(), since, bucket)
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"since": since, "bucket": bucket, "interval": int64(perf.Interval / time.Second),
		"points": points, "runs": runs, "latest": latest, "network": network})
}
