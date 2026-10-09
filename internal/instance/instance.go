// Package instance tracks one managed ReSkate server: its process, state,
// console history and player roster.
package instance

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/logfile"
	"github.com/xThrasherrr/ReSkateManager/internal/logparse"
	"github.com/xThrasherrr/ReSkateManager/internal/serverconfig"
	"github.com/xThrasherrr/ReSkateManager/internal/supervisor"
)

// State is where an instance is in its life: stopped, starting, running and so on.
type State string

// The states an instance can be in.
const (
	Stopped  State = "stopped"
	Starting State = "starting" // process running, not yet "is up on"
	Running  State = "running"
	Stopping State = "stopping"
	Crashed  State = "crashed" // exited on its own and will not be restarted
	Updating State = "updating"
)

// Def is the stored definition of an instance.
type Def struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Dir         string `json:"dir"`
	AutoStart   bool   `json:"autoStart"`
	AutoRestart bool   `json:"autoRestart"`
	// AutoUpdate installs new releases when nobody is on.
	AutoUpdate bool `json:"autoUpdate"`
	// SharedMods is whether the server links the shared mods into its Mods
	// folder, and whether one shared later loads there at once.
	SharedMods serverconfig.SharedUse `json:"sharedMods"`
	// RestartTimes are times of day ("04:00") on the manager's clock, and
	// RestartHours hours of uptime, at which the server restarts (schedule.go).
	RestartTimes []string `json:"restartTimes"`
	RestartHours int      `json:"restartHours"`
}

// Exe is the server program in the instance's folder.
func (d Def) Exe() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(d.Dir, "ReSkateServer.exe")
	}
	return filepath.Join(d.Dir, "ReSkateServer")
}

// ConfigPath is the server's ReSkateServer.json.
func (d Def) ConfigPath() string { return filepath.Join(d.Dir, "ReSkateServer.json") }

// Player is someone on the server, as the roster has them.
type Player struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Admin    bool   `json:"admin"`
	JoinedAt int64  `json:"joinedAt,omitempty"`
}

// Info is what the server announces at startup.
type Info struct {
	ServerName string `json:"serverName,omitempty"`
	Map        string `json:"map,omitempty"`
	MaxPlayers int    `json:"maxPlayers,omitempty"`
	SteamID    string `json:"steamId,omitempty"`
	PublicIP   string `json:"publicIp,omitempty"`
	JoinCode   string `json:"joinCode,omitempty"`
	Password   bool   `json:"password,omitempty"`
}

// View is the JSON state of an instance for the panel.
type View struct {
	Def
	State     State  `json:"state"`
	PID       int    `json:"pid,omitempty"`
	StartedAt int64  `json:"startedAt,omitempty"`
	ReadyAt   int64  `json:"readyAt,omitempty"`
	ExitCode  *int   `json:"exitCode,omitempty"`
	LastError string `json:"lastError,omitempty"`
	Players   int    `json:"players"`
	Info      Info   `json:"info"`
	Installed bool   `json:"installed"`
	// NextRestart is when the running server next restarts on schedule, unix ms.
	NextRestart int64 `json:"nextRestart,omitempty"`
}

const (
	consoleKeep  = 2000
	stopTimeout  = 15 * time.Second
	replyTimeout = 4 * time.Second
	stableUptime = 5 * time.Minute
)

var backoff = []time.Duration{2 * time.Second, 5 * time.Second, 15 * time.Second, 30 * time.Second, 60 * time.Second}

// Exit lines after which retrying cannot help.
var fatalPrefixes = []string{"Config problem:", "Cannot read ", "Cannot load "}

type waiter struct {
	ch       chan logparse.Entry
	internal bool // hide the reply from the console
	expires  time.Time
	// accept, when set, skips lines that can't be this command's reply, so an
	// in-game admin's result printed meanwhile isn't parsed as a roster.
	accept func(text string) bool
}

// Instance is one server: its settings, its process while it runs, and what
// it printed. Its methods are safe to call at once.
type Instance struct {
	id  string // def.ID, which never changes; apart from def so reading it needs no lock
	log *slog.Logger
	hub hub

	mu        sync.Mutex
	def       Def
	state     State
	proc      *supervisor.Process
	asm       *logparse.Assembler
	seq       uint64
	console   []logparse.Entry
	players   map[string]*Player
	info      Info
	startedAt time.Time
	readyAt   time.Time
	exitCode  *int
	lastError string
	lastLine  string
	failures  int
	retry     *time.Timer
	exited    chan struct{} // closed once watch has handled the current process's exit
	maps      []string      // the server's map list, asked once per run
	waiters   []*waiter
	voteDone  bool            // the next line is the command a passed vote ran, not a reply
	lastNet   time.Time       // when the last network summary was passed on
	clog      *logfile.Writer // console.log, while a process runs
	// cmdSem serialises commands so replies arrive in the order they were
	// sent; a channel, so someone waiting for their turn can give up.
	cmdSem chan struct{}
	// reconciling is set while a roster check is asked for and not begun.
	reconciling bool
	// stops counts Stop calls, so a restart can tell another stop came.
	stops uint64
	// OnJoin runs (in its own goroutine) for every player join; set before Start.
	OnJoin func(Player)
	// OnReady runs (in its own goroutine) each time the server finishes starting.
	OnReady func()
	// OnNetwork runs (in its own goroutine) for each network summary the
	// running server logs, with the start of its run; set before Start.
	OnNetwork func(run time.Time, n logparse.Network)
	// OnCrash runs (in its own goroutine) each time the server exits, or a
	// restart after a crash fails to start it, without anyone asking it to stop.
	OnCrash func(Crash)
	// LinkSharedMods brings the server's links to the shared mods up to date
	// before each start. It runs under the instance's lock; set before Start.
	LinkSharedMods func(Def) error
}

// New makes a stopped instance, its console seeded with the end of its log.
func New(def Def, log *slog.Logger) *Instance {
	in := &Instance{id: def.ID, def: def, state: Stopped, players: map[string]*Player{}, cmdSem: make(chan struct{}, 1), log: log.With("instance", def.ID)}
	in.loadHistory()
	return in
}

// ID is the instance's ID, which never changes.
func (in *Instance) ID() string { return in.id }

// Def is the instance's settings as they are now.
func (in *Instance) Def() Def {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.def
}

// SetDef replaces the stored settings; Dir changes only take effect on the next start.
func (in *Instance) SetDef(d Def) {
	in.mu.Lock()
	in.def = d
	in.mu.Unlock()
	in.publishState()
}

// View is the instance's state for the panel.
func (in *Instance) View() View {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.viewLocked()
}

func (in *Instance) viewLocked() View {
	v := View{Def: in.def, State: in.state, Info: in.info, ExitCode: in.exitCode, LastError: in.lastError, Players: len(in.players)}
	if in.proc != nil {
		v.PID = in.proc.PID()
	}
	if !in.startedAt.IsZero() {
		v.StartedAt = in.startedAt.UnixMilli()
	}
	if !in.readyAt.IsZero() {
		v.ReadyAt = in.readyAt.UnixMilli()
	}
	if in.state == Running {
		if next := in.def.NextRestart(in.startedAt, time.Now()); !next.IsZero() {
			v.NextRestart = next.UnixMilli()
		}
	}
	_, err := os.Stat(in.def.Exe())
	v.Installed = err == nil
	return v
}

// Usage reads the server process's CPU time and memory, with the view taken
// at the same moment. It fails when no process is running.
func (in *Instance) Usage() (supervisor.Usage, View, error) {
	in.mu.Lock()
	p, v := in.proc, in.viewLocked()
	in.mu.Unlock()
	if p == nil {
		return supervisor.Usage{}, v, errors.New("server is not running")
	}
	u, err := p.Usage()
	return u, v, err
}

// State is where the instance is now.
func (in *Instance) State() State {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.state
}

// Players lists who is on, in no particular order.
func (in *Instance) Players() []Player {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.playersLocked()
}

func (in *Instance) playersLocked() []Player {
	out := make([]Player, 0, len(in.players))
	for _, p := range in.players {
		out = append(out, *p)
	}
	return out
}

// Console returns the kept history, oldest first.
func (in *Instance) Console() []logparse.Entry {
	in.mu.Lock()
	defer in.mu.Unlock()
	return append([]logparse.Entry(nil), in.console...)
}

// Subscribe returns live events plus a snapshot taken atomically with the subscription.
func (in *Instance) Subscribe() (ch chan Event, view View, console []logparse.Entry, players []Player) {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.hub.subscribe(), in.viewLocked(), append([]logparse.Entry(nil), in.console...), in.playersLocked()
}

// Unsubscribe ends a subscription Subscribe made.
func (in *Instance) Unsubscribe(ch chan Event) { in.hub.unsubscribe(ch) }

// ---- lifecycle ----

// ErrBusy refuses what the server's state rules out for now: a start while
// it stops, an update while it runs.
var ErrBusy = errors.New("server is busy")

// ErrNoReply is a command the server said nothing to in time. It may still
// have been carried out.
var ErrNoReply = errors.New("no reply from the server")

// Start starts the server. It does nothing when the server is up already, and
// returns ErrBusy while it stops or is being updated.
func (in *Instance) Start() error {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.retry != nil {
		in.retry.Stop()
		in.retry = nil
	}
	switch in.state {
	case Starting, Running:
		return nil
	case Stopping, Updating:
		return ErrBusy
	}
	in.failures = 0
	return in.startLocked()
}

func (in *Instance) startLocked() error {
	exe := in.def.Exe()
	if _, err := os.Stat(exe); err != nil {
		in.lastError = "server files are not installed in " + in.def.Dir
		in.state = Stopped
		in.publishStateLocked()
		return errors.New(in.lastError)
	}
	in.asm = logparse.NewAssembler()
	in.players = map[string]*Player{}
	in.info = Info{}
	in.exitCode, in.lastError, in.lastLine = nil, "", ""
	in.readyAt = time.Time{}
	in.maps = nil
	in.state = Starting
	if c, err := logfile.Open(filepath.Join(in.def.Dir, ConsoleLog)); err != nil {
		in.managerLineLocked("Could not open " + ConsoleLog + ": " + err.Error())
	} else {
		in.clog = c
	}
	if msg, err := rotateLog(in.def.Dir); err != nil {
		in.managerLineLocked("Could not rotate " + ServerLog + ": " + err.Error())
	} else if msg != "" {
		in.managerLineLocked(msg)
	}
	if msg, err := linkSteamClient(in.def.Dir); err != nil {
		in.managerLineLocked("Could not link steamclient.so for Steam: " + err.Error())
	} else if msg != "" {
		in.managerLineLocked(msg)
	}
	if in.LinkSharedMods != nil {
		if err := in.LinkSharedMods(in.def); err != nil {
			in.managerLineLocked("Could not link the shared mods: " + err.Error())
		}
	}
	if msg, err := fixServerName(in.def.ConfigPath()); err != nil {
		in.managerLineLocked("Could not fix the server name: " + err.Error())
	} else if msg != "" {
		in.managerLineLocked(msg)
	}
	in.managerLineLocked("Starting " + exe)
	p, err := supervisor.Start(supervisor.Options{Exe: exe, Dir: in.def.Dir}, in.onLine, in.onIdle)
	if err != nil {
		in.state = Crashed
		in.lastError = err.Error()
		in.managerLineLocked("Could not start: " + err.Error())
		in.closeConsoleLocked()
		in.publishStateLocked()
		return err
	}
	in.proc = p
	in.startedAt = time.Now()
	in.publishStateLocked()
	in.log.Info("server started", "pid", p.PID())
	in.exited = make(chan struct{})
	go in.watch(p, in.exited)
	return nil
}

// Stop asks the server to quit and waits for it.
func (in *Instance) Stop(ctx context.Context) error {
	in.mu.Lock()
	in.stops++
	if in.retry != nil {
		in.retry.Stop()
		in.retry = nil
		if in.state != Running && in.state != Starting {
			in.state = Stopped
			in.publishStateLocked()
		}
	}
	p, exited := in.proc, in.exited
	if p == nil || in.state == Stopping {
		in.mu.Unlock()
		if p != nil {
			return wait(ctx, exited)
		}
		return nil
	}
	in.state = Stopping
	in.managerLineLocked("Stopping the server...")
	in.publishStateLocked()
	in.mu.Unlock()
	go p.Stop(stopTimeout)
	return wait(ctx, exited)
}

// wait blocks until watch has handled the exit, so the caller sees Stopped
// and no process, not just a dead one.
func wait(ctx context.Context, exited <-chan struct{}) error {
	select {
	case <-exited:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Restart stops the server and starts it again, unless someone stopped it
// meanwhile: their stop stands.
func (in *Instance) Restart(ctx context.Context) error {
	in.mu.Lock()
	stops := in.stops
	in.mu.Unlock()
	if err := in.Stop(ctx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	in.mu.Lock()
	stoppedMeanwhile := in.stops != stops+1
	in.mu.Unlock()
	if stoppedMeanwhile {
		return errors.New("someone else stopped the server meanwhile, so it stays stopped")
	}
	return in.Start()
}

// Kill ends the process without a graceful quit.
func (in *Instance) Kill() {
	in.mu.Lock()
	p := in.proc
	if p != nil {
		in.state = Stopping
		in.managerLineLocked("Killing the server process.")
		in.publishStateLocked()
	}
	in.mu.Unlock()
	if p != nil {
		p.Kill()
	}
}

func (in *Instance) watch(p *supervisor.Process, exited chan struct{}) {
	defer close(exited) // after the unlock below: by then the exit is handled
	<-p.Done()
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.proc != p {
		return
	}
	defer in.closeConsoleLocked() // after the lines about the exit
	code := p.ExitCode()
	in.exitCode = &code
	in.proc = nil
	in.players = map[string]*Player{}
	in.failWaitersLocked()
	requested := in.state == Stopping || in.state == Updating
	wasReady := !in.readyAt.IsZero()
	uptime := time.Since(in.readyAt)
	in.managerLineLocked(fmt.Sprintf("Server exited with code %d.", code))
	in.log.Info("server exited", "code", code, "requested", requested)

	if requested {
		if in.state == Stopping {
			in.state = Stopped
		}
		in.publishStateLocked()
		return
	}
	in.lastError = in.lastLine
	// A setup problem stops a server before it is ready. Once ready, the last
	// line can be a player's join with any name, "Config problem: ..." too.
	fatal := !wasReady && slices.ContainsFunc(fatalPrefixes, func(pre string) bool { return strings.HasPrefix(in.lastLine, pre) })
	if wasReady && uptime > stableUptime {
		in.failures = 0
	}
	crash := Crash{Code: code, Reason: in.lastLine, Fatal: fatal}
	if !in.def.AutoRestart || fatal || in.failures >= len(backoff) {
		in.state = Crashed
		if fatal {
			in.managerLineLocked("Not restarting: fix the problem above, then start the server.")
		} else if in.def.AutoRestart {
			in.managerLineLocked("Not restarting: it failed too many times in a row.")
			crash.GaveUp, crash.Of = true, len(backoff)
		}
		in.publishStateLocked()
		in.crashedLocked(crash)
		return
	}
	delay := backoff[in.failures]
	in.failures++
	in.state = Crashed
	in.managerLineLocked(fmt.Sprintf("Restarting in %s (attempt %d of %d).", delay, in.failures, len(backoff)))
	in.publishStateLocked()
	crash.Retry, crash.Attempt, crash.Of = delay, in.failures, len(backoff)
	in.crashedLocked(crash)
	in.retry = time.AfterFunc(delay, func() {
		in.mu.Lock()
		defer in.mu.Unlock()
		if in.retry == nil || in.state != Crashed {
			return
		}
		in.retry = nil
		if err := in.startLocked(); err != nil {
			in.crashedLocked(Crash{Code: -1, Reason: err.Error(), NoStart: true})
		}
	})
}

// Crash is a server stopping, or failing to start after a crash, when nobody
// asked it to.
type Crash struct {
	Code    int           // its exit code; -1 when it never started
	Reason  string        // its last line, or why it could not start
	Fatal   bool          // a problem retrying cannot fix, such as its config
	NoStart bool          // a restart after a crash could not start it
	GaveUp  bool          // it crashed too often in a row to try again
	Retry   time.Duration // when it restarts; 0 when it does not
	Attempt int           // which restart in a row that is,
	Of      int           // out of how many it tries
}

func (in *Instance) crashedLocked(c Crash) {
	if in.OnCrash != nil {
		go in.OnCrash(c)
	}
}

// IfStopped runs fn while no server process exists, and keeps one from
// starting until fn returns. ran is false when the server is running.
// fn runs under the instance lock, so it must not call other methods on in.
func (in *Instance) IfStopped(fn func() error) (ran bool, err error) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.proc != nil || in.state == Updating {
		return false, nil
	}
	return true, fn()
}

// BeginUpdate moves a stopped server to Updating so nothing starts it mid-install.
func (in *Instance) BeginUpdate() error {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.proc != nil || in.state == Updating {
		return ErrBusy
	}
	if in.retry != nil {
		in.retry.Stop()
		in.retry = nil
	}
	in.state = Updating
	in.publishStateLocked()
	return nil
}

// EndUpdate lets a server BeginUpdate held be started again.
func (in *Instance) EndUpdate() {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.state == Updating {
		in.state = Stopped
		in.publishStateLocked()
	}
}

// Note adds a manager line to the console.
func (in *Instance) Note(text string) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.managerLineLocked(text)
}

// ---- output ----

func (in *Instance) onLine(line string) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if e := in.asm.Push(line); e != nil {
		in.handleLocked(*e)
	}
}

func (in *Instance) onIdle() {
	in.mu.Lock()
	defer in.mu.Unlock()
	if e := in.asm.Flush(); e != nil {
		in.handleLocked(*e)
	}
}

func (in *Instance) handleLocked(e logparse.Entry) {
	in.lastLine, _, _ = strings.Cut(e.Text, "\n")
	if in.voteDone {
		// A passed vote logs its command's result straight after.
		in.voteDone = false
		if !e.IsEvent() {
			e.Kind, e.Tag = logparse.KindTagged, "vote"
		}
	}
	hidden := false
	if !e.IsEvent() {
		hidden = in.deliverReplyLocked(e)
		// Whoever ran it (the panel, the console, the admin sync), the roster
		// follows an admin change made while the player is on.
		if who, admin, ok := logparse.ParseAdminChange(e.Text); ok {
			in.setAdminLocked(who, admin)
		}
	}
	// What only startup prints is believed only during startup: later, a line
	// that looks like it can come from a player's name in a command's reply.
	if !in.startupLineLocked(e.Kind) {
		e.Kind = logparse.KindRaw
	}
	switch e.Kind {
	case logparse.KindTagged:
		in.voteDone = e.Tag == "vote" && votePassedRe.MatchString(e.Text)
		if e.Tag == "network" {
			in.networkLocked(e.Text)
		}
	case logparse.KindReady:
		in.state = Running
		in.readyAt = time.Now()
		in.info.ServerName = e.Name
		in.info.Map = e.Fields["map"]
		in.info.MaxPlayers, _ = strconv.Atoi(e.Fields["max"])
		in.players = map[string]*Player{}
		in.publishStateLocked()
		if in.OnReady != nil {
			go in.OnReady()
		}
	case logparse.KindSteam:
		in.info.SteamID, in.info.PublicIP = e.ID, e.Fields["public_ip"]
		in.publishStateLocked()
	case logparse.KindJoinCode:
		in.info.JoinCode, in.info.Password = e.Fields["code"], e.Fields["password"] == "1"
		in.publishStateLocked()
	case logparse.KindLoaded:
		in.info.Map = e.Fields["map"]
		in.publishStateLocked()
	case logparse.KindJoin:
		p := &Player{ID: e.ID, Name: e.Name, Admin: e.Fields["admin"] == "1", JoinedAt: time.Now().UnixMilli()}
		in.players[e.ID] = p
		in.publishPlayersLocked()
		if in.OnJoin != nil {
			go in.OnJoin(*p)
		}
		if n, _ := strconv.Atoi(e.Fields["players"]); n != len(in.players) {
			in.reconcileLocked()
		}
	case logparse.KindLeave:
		matches := 0
		for id, p := range in.players {
			if p.Name == e.Name {
				if matches == 0 {
					delete(in.players, id)
				}
				matches++
			}
		}
		in.publishPlayersLocked()
		if matches != 1 {
			in.reconcileLocked()
		}
	}
	if !hidden {
		in.appendLocked(e)
	}
}

func (in *Instance) appendLocked(e logparse.Entry) {
	in.seq++
	e.Seq = in.seq
	if e.At == 0 {
		e.At = time.Now().UnixMilli()
	}
	if len(in.console) >= consoleKeep {
		in.console = append(in.console[:0], in.console[len(in.console)-consoleKeep/2:]...)
	}
	in.console = append(in.console, e)
	in.hub.publish(Event{Type: "console", Instance: in.def.ID, Entry: e})
	if in.clog != nil {
		if _, err := in.clog.Write([]byte(logparse.FormatLine(e))); err != nil {
			in.log.Warn("console.log stopped", "err", err)
			in.closeConsoleLocked()
		}
	}
}

func (in *Instance) closeConsoleLocked() {
	if in.clog != nil {
		in.clog.Close()
		in.clog = nil
	}
}

func (in *Instance) managerLineLocked(text string) {
	in.appendLocked(logparse.Entry{Kind: logparse.KindManager, Text: text, Stamp: time.Now().Format("15:04:05")})
}

func (in *Instance) publishState() {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.publishStateLocked()
}

// netEvery is the least time between network summaries passed on. The
// server logs one a minute; one sooner isn't the server's own.
const netEvery = 30 * time.Second

// networkLocked passes on a network summary the running server logged.
func (in *Instance) networkLocked(text string) {
	n, ok := logparse.ParseNetwork(text)
	now := time.Now()
	if !ok || in.state != Running || in.OnNetwork == nil || now.Sub(in.lastNet) < netEvery {
		return
	}
	in.lastNet = now
	go in.OnNetwork(in.startedAt, n)
}

func (in *Instance) publishStateLocked() {
	v := in.viewLocked()
	in.hub.publish(Event{Type: "state", Instance: in.def.ID, State: &v})
}

func (in *Instance) publishPlayersLocked() {
	in.hub.publish(Event{Type: "players", Instance: in.def.ID, Players: in.playersLocked()})
}

// ---- commands ----

// Command sends a console line and waits for the server's reply. `by` labels
// who sent it in the console view. Lifecycle words are handled by the
// manager, never passed through: the server's own update would relaunch it
// outside the manager's control.
func (in *Instance) Command(ctx context.Context, line, by string) (string, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", errors.New("empty command")
	}
	switch strings.ToLower(strings.Fields(line)[0]) {
	case "quit", "exit", "stop":
		return "", errors.New("use the Stop button to stop the server")
	case "update":
		return "", errors.New("updates are installed by the manager")
	}
	return in.send(ctx, line, by, false, nil)
}

// votePassedRe is a vote that passed. What it ran logs its result straight
// after: a map or time of day vote's command, or since 2.0.2 a custom vote's.
// A kick vote's leave line comes there instead, which is an event anyway.
var votePassedRe = regexp.MustCompile(`^\[vote\] The vote to .+ passed \(\d+ yes, \d+ no, \d+ needed\)\.$`)

// replyShape accepts a reply whose first line matches re, or a refusal.
func replyShape(re *regexp.Regexp) func(string) bool {
	return func(text string) bool {
		first, _, _ := strings.Cut(text, "\n")
		return re.MatchString(first) || logparse.IsCommandError(text)
	}
}

func (in *Instance) send(ctx context.Context, line, by string, internal bool, accept func(string) bool) (string, error) {
	select {
	case in.cmdSem <- struct{}{}:
	case <-ctx.Done():
		return "", errors.New("the server is still busy with an earlier command")
	}
	defer func() { <-in.cmdSem }()
	// The caller may have given up while an earlier command held the line;
	// sent now, this one would run with nobody waiting for its reply.
	if err := ctx.Err(); err != nil {
		return "", err
	}
	in.mu.Lock()
	p := in.proc
	if p == nil || in.state != Running {
		in.mu.Unlock()
		if p != nil && in.state == Starting {
			return "", errors.New("the server is still starting")
		}
		return "", errors.New("server is not running")
	}
	w := &waiter{ch: make(chan logparse.Entry, 1), internal: internal, expires: time.Now().Add(15 * time.Second), accept: accept}
	in.waiters = append(in.waiters, w)
	if !internal {
		shown := line
		if verb, _, _ := strings.Cut(line, " "); strings.EqualFold(verb, "password") && !strings.EqualFold(strings.TrimSpace(line), "password off") && strings.Contains(line, " ") {
			shown = "password <hidden>"
		}
		in.appendLocked(logparse.Entry{Kind: logparse.KindInput, Text: shown, Name: by, Stamp: time.Now().Format("15:04:05")})
	}
	in.mu.Unlock()
	if err := p.Send(line); err != nil {
		in.mu.Lock()
		in.dropWaiterLocked(w)
		in.mu.Unlock()
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, replyTimeout)
	defer cancel()
	select {
	case e, ok := <-w.ch:
		if !ok {
			return "", errors.New("server stopped before replying")
		}
		return e.Text, nil
	case <-ctx.Done():
		// The waiter stays queued until it expires so a late reply is still
		// matched to this command rather than to the next one.
		return "", ErrNoReply
	}
}

func (in *Instance) deliverReplyLocked(e logparse.Entry) (hidden bool) {
	now := time.Now()
	for len(in.waiters) > 0 {
		w := in.waiters[0]
		if now.After(w.expires) {
			in.waiters = in.waiters[1:]
			close(w.ch)
			continue
		}
		if w.accept != nil && !w.accept(e.Text) {
			return false
		}
		in.waiters = in.waiters[1:]
		w.ch <- e
		close(w.ch)
		return w.internal
	}
	return false
}

func (in *Instance) dropWaiterLocked(w *waiter) {
	for i, x := range in.waiters {
		if x == w {
			in.waiters = append(in.waiters[:i], in.waiters[i+1:]...)
			close(w.ch)
			return
		}
	}
}

func (in *Instance) failWaitersLocked() {
	for _, w := range in.waiters {
		close(w.ch)
	}
	in.waiters = nil
}

// setAdminLocked marks an online player, by SteamID64 or name, as an admin or not.
func (in *Instance) setAdminLocked(id string, admin bool) {
	p := in.players[id]
	if p == nil || p.Admin == admin {
		return
	}
	p.Admin = admin
	in.publishPlayersLocked()
}

var playersReplyRe = regexp.MustCompile(`^\d+ players$`)

// Reconcile replaces the roster with the server's own `players` list. The
// reply is hidden from the console but still lands in ReSkateServer.log, so
// it runs only when the join/leave lines disagree with the server's count.
func (in *Instance) Reconcile() {
	in.mu.Lock()
	in.reconciling = false
	in.mu.Unlock()
	reply, err := in.send(context.Background(), "players", "", true, replyShape(playersReplyRe))
	if err != nil {
		return
	}
	rows, ok := logparse.ParsePlayers(reply)
	if !ok {
		return
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	next := map[string]*Player{}
	for _, r := range rows {
		p := &Player{ID: r.ID, Name: r.Name, Admin: r.Admin, JoinedAt: time.Now().UnixMilli()}
		if old, ok := in.players[r.ID]; ok {
			p.JoinedAt = old.JoinedAt
		} else if in.OnJoin != nil {
			// A join the console showed some other way (its line taken for
			// another kind, say): still a player who was here.
			go in.OnJoin(*p)
		}
		next[r.ID] = p
	}
	in.players = next
	in.publishPlayersLocked()
}

// startupLineLocked reports whether a line of kind k can be the server's own
// at this point. It prints that it is up while starting, and its Steam ID and
// join code straight after (Server/main.cpp); a player can't be on by then.
func (in *Instance) startupLineLocked(k logparse.Kind) bool {
	switch k {
	case logparse.KindReady:
		return in.state == Starting
	case logparse.KindSteam, logparse.KindJoinCode:
		return in.state == Starting || in.state == Running && time.Since(in.readyAt) < 10*time.Second
	}
	return true
}

// reconcileLocked asks for the roster once, however many mismatches ask
// before the answer: each would be a `players` command in the server's log
// and a turn on the console. The caller holds mu.
func (in *Instance) reconcileLocked() {
	if in.reconciling {
		return
	}
	in.reconciling = true
	go in.Reconcile()
}
