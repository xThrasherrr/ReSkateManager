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
	ID   string `json:"id"`
	Name string `json:"name"`
	// Role is the one they were given on a server with roles (ReSkate
	// 2.0.6), not the default one everybody else has; "admin" for an admin
	// before then.
	Role     string `json:"role,omitempty"`
	Admin    bool   `json:"admin"`
	JoinedAt int64  `json:"joinedAt,omitempty"`
}

// Leave is a player going from the roster: they left, or it emptied without
// saying so, as when the server stops.
type Leave struct {
	Player
	At     int64  // unix ms
	Reason string // the server's, as "Disconnected.", or one of the Left reasons
}

// Notice is something that happened on the server, for the plugins. Kind
// says which, and which fields are set.
type Notice struct {
	Kind      string             // NoticeJoined, NoticeLeft, ...
	Player    Player             // joined, left, chat and command; a chat's ID is "" when its name could be more than one
	Leave     Leave              // left
	Text      string             // chat: the message; command: what followed the word
	Command   string             // command: the word a player typed, one of a plugin's
	Plugin    string             // command: the plugin it's for
	Map       string             // map, and state: the map loaded
	Throwdown logparse.Throwdown // throwdown
	State     State              // state: now, and before
	Was       State
}

// The kinds of Notice.
const (
	NoticeJoined    = "joined"
	NoticeLeft      = "left"
	NoticeChat      = "chat"
	NoticeThrowdown = "throwdown"
	NoticeMap       = "map"
	NoticeState     = "state"
	NoticeCommand   = "command"
)

// Why the roster let a player go when the server never said they left.
const (
	LeftStopped  = "Server stopped."
	LeftExited   = "Server exited." // on its own: crashed, or quit from the game
	LeftRestart  = "Server restarted."
	LeftNotOn    = "Not on the server." // missing from its players list, or joined again
	LeftUnclosed = "Manager stopped."   // still open when the manager next started
)

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

// lostConsole is why a server whose output closed while it ran was ended
// (supervisor.Process.Lost). Restarting it would only do the same again.
const lostConsole = "The server closed its console while running, so the manager could neither read nor stop it, and ended it. " +
	"ReSkate's Windows server did this in 2.0.6 and 2.0.7; 2.0.8 fixed it, so update the server on its Updates tab."

type waiter struct {
	ch       chan logparse.Entry
	line     string // the command it waits on the reply to
	internal bool   // hide the reply from the console
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
	maps      []string      // the server's map list, asked once per run and again after it reads Mods again
	mapsGen   int           // how many times the server read Mods again this run, so a list asked before isn't kept
	// mapsMoved: a mod went to a newer version under maps.latest_versions
	// (2.0.7), and the server reads Mods again by itself next.
	mapsMoved bool
	waiters   []*waiter
	ranBy     string         // "vote" or "poll": the next line is a command a passed vote or poll-run ran, not a reply
	pollRun   bool           // the open poll is the console's poll-run, whose winning answer runs a command
	runs      map[string]serverconfig.CustomCommand // the server's chat commands, by name; nil until one runs
	results   int            // results still to come of the commands a chat command runs, not replies either
	// defaultRole is the role of everyone on the server given none (2.0.6),
	// read as it starts.
	defaultRole string
	// wordsShown: the server printed what words answers as it started
	// (2.0.6), which it does once, before it reads any command.
	wordsShown bool
	lastNet    time.Time       // when the last network summary was passed on
	clog       *logfile.Writer // console.log, while a process runs
	// cmdSem serialises commands so replies arrive in the order they were
	// sent; a channel, so someone waiting for their turn can give up.
	cmdSem chan struct{}
	cards  cards // the server's one announcement card, which Announce waits for
	// reconciling is set while a roster check is asked for and not begun.
	reconciling bool
	// stops counts Stop calls, so a restart can tell another stop came.
	stops uint64
	// unmatched are leave lines that named more than one player, kept until
	// the roster check finds out which of them it was.
	unmatched []Leave
	// gone are the players who left this run, newest last: the server still
	// names them in its lines, a throwdown's results for one.
	gone []Player
	// hooks are the OnJoin and OnLeave calls still running.
	hooks sync.WaitGroup
	// OnJoin runs (in its own goroutine) for every player join; set before Start.
	OnJoin func(Player)
	// OnLeave runs (in its own goroutine) for every player OnJoin had, as they
	// go from the roster; set before Start.
	OnLeave func(Leave)
	// OnThrowdown runs (in its own goroutine) for each [throwdown] line, with
	// the players matched to their SteamIDs where that can be told; set before
	// Start.
	OnThrowdown func(logparse.Throwdown)
	// OnReady runs (in its own goroutine) each time the server finishes starting.
	OnReady func()
	// OnNotice runs for each Notice, in the order they happen, under the
	// instance's lock: it must return at once and never call back into the
	// instance. Set before Start.
	OnNotice func(Notice)
	noticed  State // the state OnNotice last heard of
	// OnNetwork runs (in its own goroutine) for each network summary the
	// running server logs, with the start of its run; set before Start.
	OnNetwork func(run time.Time, n logparse.Network)
	// OnCrash runs (in its own goroutine) each time the server exits, or a
	// restart after a crash fails to start it, without anyone asking it to stop.
	OnCrash func(Crash)
	// LinkSharedMods brings the server's links to the shared mods up to date
	// before each start. It runs under the instance's lock; set before Start.
	LinkSharedMods func(Def) error
	// PluginCommands puts the plugins' chat commands in the server's config
	// before each start, and says what it couldn't. It runs under the
	// instance's lock; set before Start.
	PluginCommands func(Def) (string, error)
}

// New makes a stopped instance, its console seeded with the end of its log.
func New(def Def, log *slog.Logger) *Instance {
	in := &Instance{id: def.ID, def: def, state: Stopped, noticed: Stopped, players: map[string]*Player{}, cmdSem: make(chan struct{}, 1), log: log.With("instance", def.ID)}
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
	in.emptyRosterLocked(LeftRestart)
	in.gone = nil // a new process knows nobody's name
	in.info = Info{}
	in.exitCode, in.lastError, in.lastLine = nil, "", ""
	in.readyAt = time.Time{}
	in.wordsShown = false
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
	if in.PluginCommands != nil {
		if msg, err := in.PluginCommands(in.def); err != nil {
			in.managerLineLocked("Could not put the plugins' chat commands in: " + err.Error())
		} else if msg != "" {
			in.managerLineLocked(msg)
		}
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
	in.failWaitersLocked()
	requested := in.state == Stopping || in.state == Updating
	if requested {
		in.emptyRosterLocked(LeftStopped)
	} else {
		in.emptyRosterLocked(LeftExited)
	}
	wasReady := !in.readyAt.IsZero()
	uptime := time.Since(in.readyAt)
	if p.Lost() {
		in.lastLine = lostConsole
		in.managerLineLocked(lostConsole)
	}
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
	fatal := p.Lost() || !wasReady && slices.ContainsFunc(fatalPrefixes, func(pre string) bool { return strings.HasPrefix(in.lastLine, pre) })
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

// ErrJustStarted is why EditStopped changed nothing: the server started.
var ErrJustStarted = errors.New("the server just started; try again")

// EditStopped makes fn's change to a stopped server's config, while it can't
// start, and returns fn's reply.
func (in *Instance) EditStopped(fn func(*serverconfig.File) (string, error)) (string, error) {
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
		return "", ErrJustStarted
	}
	return reply, err
}

// ChangeRoles makes change to the server's data/roles.json as its own roles
// editor does: a running server reads the file again (roles reload), sent as
// by, so its players see their tags change at once, and a stopped one reads
// it as it starts.
func (in *Instance) ChangeRoles(ctx context.Context, by string, change func(*serverconfig.File) (string, error)) (string, error) {
	if s := in.State(); s != Running && s != Starting {
		return in.EditStopped(change)
	}
	// A running server writes the file itself as roles are given, so it's
	// read just before.
	path := in.Def().ConfigPath()
	f, err := serverconfig.Read(path)
	if err != nil {
		return "", err
	}
	reply, err := change(f)
	if err != nil {
		return "", err
	}
	if err := f.WriteRoles(path); err != nil {
		return "", err
	}
	answer, err := in.Command(ctx, "roles reload", by)
	if err == nil && strings.Contains(answer, "was not loaded") {
		err = errors.New(answer) // the server keeps the roles it had
	}
	return reply, err
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
	// What only startup prints is believed only during startup: later, a line
	// that looks like it can come from a player's name in a command's reply,
	// or be the reply.
	if !in.startupLineLocked(e) {
		e.Kind = logparse.KindRaw
	}
	// From 2.0.7 the server reads Mods again while it runs: for maps reload
	// (from here or in the game), and by itself once maps.latest_versions has
	// moved a mod on, which it says first. Its maps change, and only the
	// first is a reply.
	switch {
	case strings.HasPrefix(e.Text, logparse.MapsReread):
		in.maps = nil
		in.mapsGen++
		if in.mapsMoved {
			in.mapsMoved = false
			e.Kind = logparse.KindStartup
		}
	case e.Kind == logparse.KindStartup && strings.HasPrefix(e.Text, logparse.MapVersions) && strings.Contains(e.Text, " is now at "):
		in.mapsMoved = true
	}
	if in.ranBy != "" {
		// A passed vote, or a poll-run's winner, logs its command's result
		// straight after.
		if !e.IsEvent() {
			e.Kind, e.Tag = logparse.KindTagged, in.ranBy
		}
		in.ranBy = ""
	} else if in.results > 0 && !e.IsEvent() {
		// Each command one of the owner's chat commands runs logs its result,
		// with any lines of the command's own before it.
		in.results--
		e.Kind, e.Tag = logparse.KindTagged, "command"
	}
	// Whoever started it: the panel, or a chat command or custom vote that
	// runs a poll-run.
	if strings.HasPrefix(e.Text, "Poll started; the winning answer runs: ") {
		in.pollRun = true
	}
	hidden := false
	if !e.IsEvent() || in.taggedReplyLocked(e) {
		var answered string
		hidden, answered = in.deliverReplyLocked(e)
		// Whoever ran it (the panel, the console, the admin sync), the roster
		// follows a role given while the player is on.
		if who, role, ok := logparse.ParseRoleChange(answered, e.Text); ok {
			in.setRoleLocked(who, role)
		}
	}
	switch e.Kind {
	case logparse.KindTagged:
		switch {
		case e.Tag == "vote" && votePassedRe.MatchString(e.Text):
			in.ranBy = "vote"
		case e.Tag == "vote" && strings.Contains(e.Text, " was cancelled: "):
			in.pollRun = false
		case e.Tag == "poll" && pollEndRe.MatchString(e.Text):
			// Only a clear winner runs anything: not a tie, nor nobody answering.
			if in.pollRun && strings.HasSuffix(e.Text, " wins).") {
				in.ranBy = "poll"
			}
			in.pollRun = false
		case e.Tag == "network":
			in.networkLocked(e.Text)
		case e.Tag == "announcement":
			in.sawCard(e.Text)
		case e.Tag == "throwdown" && (in.OnThrowdown != nil || in.OnNotice != nil):
			if t, ok := logparse.ParseThrowdown(e.Text, in.namesLocked()); ok {
				if in.OnThrowdown != nil {
					in.hooks.Go(func() { in.OnThrowdown(t) })
				}
				in.noticeLocked(Notice{Kind: NoticeThrowdown, Throwdown: t})
			}
		}
	case logparse.KindChat:
		if in.OnNotice != nil && e.IsEvent() && e.Fields != nil {
			in.noticeLocked(Notice{Kind: NoticeChat, Player: Player{ID: in.namesLocked()(e.Name), Name: e.Name}, Text: e.Fields["message"]})
		}
	case logparse.KindCommand, logparse.KindAdmin:
		// A role given in game is answered to the admin alone, so the roster
		// asks the server who has which role instead.
		verb, rest, _ := strings.Cut(strings.ToLower(strings.TrimPrefix(e.Fields["message"], "/")), " ")
		if verb == "role" || verb == "roles" && strings.TrimSpace(rest) == "reload" {
			in.reconcileLocked()
		}
		if name := e.Fields["custom"]; name != "" {
			// The server reads its chat commands only when it starts.
			if in.runs == nil {
				in.runs = serverconfig.ChatCommands(in.def.ConfigPath())
			}
			c := in.runs[name]
			in.results = len(c.Command)
			if plugin := c.Plugin(); plugin != "" && in.OnNotice != nil {
				args := strings.TrimSpace(strings.TrimPrefix(e.Fields["message"], "/"+name))
				in.noticeLocked(Notice{Kind: NoticeCommand, Player: Player{ID: in.namesLocked()(e.Name), Name: e.Name}, Command: name, Plugin: plugin, Text: args})
			}
		}
	case logparse.KindReady:
		in.runs, in.results, in.pollRun, in.mapsMoved = nil, 0, false, false
		in.cards.clear()
		in.defaultRole = serverconfig.DefaultRole(in.def.ConfigPath())
		in.state = Running
		in.readyAt = time.Now()
		in.info.ServerName = e.Name
		in.info.Map = e.Fields["map"]
		in.info.MaxPlayers, _ = strconv.Atoi(e.Fields["max"])
		in.emptyRosterLocked(LeftRestart)
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
		in.noticeLocked(Notice{Kind: NoticeMap, Map: in.info.Map})
	case logparse.KindJoin:
		p := in.newPlayerLocked(e.ID, e.Name, e.Fields["role"])
		if old := in.players[e.ID]; old != nil { // a leave the console didn't show
			in.leftLocked(Leave{Player: *old, At: p.JoinedAt, Reason: LeftNotOn})
		}
		in.players[e.ID] = p
		in.publishPlayersLocked()
		in.joinedLocked(*p)
		if n, _ := strconv.Atoi(e.Fields["players"]); n != len(in.players) {
			in.reconcileLocked()
		}
	case logparse.KindLeave:
		// The line names the player, not their SteamID: with two of that
		// name on, the roster check finds out which one went.
		var gone []*Player
		for _, p := range in.players {
			if p.Name == e.Name {
				gone = append(gone, p)
			}
		}
		l := Leave{Player: Player{Name: e.Name}, At: time.Now().UnixMilli(), Reason: e.Fields["reason"]}
		if len(gone) == 1 {
			delete(in.players, gone[0].ID)
			in.publishPlayersLocked()
			l.Player = *gone[0]
			in.leftLocked(l)
			break
		}
		if len(gone) > 1 {
			in.unmatched = append(in.unmatched, l)
		}
		in.reconcileLocked()
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
	if in.state != in.noticed { // every change of state is published
		was := in.noticed
		in.noticed = in.state
		in.noticeLocked(Notice{Kind: NoticeState, State: in.state, Was: was, Map: in.info.Map})
	}
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
	if err := lifecycle(line); err != nil {
		return "", err
	}
	// "poll-run <command> | <question> | <answer>...", 2.0.3: the winning
	// answer goes in for {answer}, and the command runs as the console's.
	if verb, rest, _ := strings.Cut(line, " "); strings.EqualFold(verb, "poll-run") {
		parts := strings.Split(rest, "|")
		for _, answer := range parts[min(2, len(parts)):] {
			if err := lifecycle(strings.ReplaceAll(parts[0], "{answer}", strings.TrimSpace(answer))); err != nil {
				return "", fmt.Errorf("a poll-run can't end the server's run: %w", err)
			}
		}
	}
	return in.send(ctx, line, by, false, nil)
}

// lifecycle refuses a command that would stop or update the server.
func lifecycle(line string) error {
	verb, _, _ := strings.Cut(strings.TrimSpace(line), " ")
	switch strings.ToLower(verb) {
	case "quit", "exit", "stop":
		return errors.New("use the Stop button to stop the server")
	case "update":
		return errors.New("updates are installed by the manager")
	}
	return nil
}

// votePassedRe is a vote that passed. What it ran logs its result straight
// after: a map or time of day vote's command, since 2.0.2 a custom vote's,
// and since 2.0.7 an extend vote's "rotation extend". A kick vote's leave
// line comes there instead, which is an event anyway. From 2.0.7 the tally
// counts the votes cast: "2 needed of 3 votes".
var votePassedRe = regexp.MustCompile(`^\[vote\] The vote to .+ passed \(\d+ yes, \d+ no, \d+ needed(?: of \d+ votes?)?\)\.$`)

// pollEndRe is a poll ending, which since 2.0.3 runs a poll-run's command
// for the winning answer straight after.
var pollEndRe = regexp.MustCompile(`^\[poll\] Poll ".*" ended: `)

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
	w := &waiter{ch: make(chan logparse.Entry, 1), line: line, internal: internal, expires: time.Now().Add(15 * time.Second), accept: accept}
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

// taggedReplies are the commands whose reply from the console is a tagged
// line, which is otherwise the server's own: park-mod's is the "[parks]" line
// it logs for an admin in the game (2.0.6).
var taggedReplies = map[string]string{"park-mod": "parks"}

// taggedReplyLocked reports whether a tagged line is the reply to the command
// waiting for one.
func (in *Instance) taggedReplyLocked(e logparse.Entry) bool {
	return e.Kind == logparse.KindTagged && taggedReplies[in.waitingLocked()] == e.Tag
}

// waitingLocked is the verb of the command the next reply is for, "" when
// none is waiting.
func (in *Instance) waitingLocked() string {
	now := time.Now()
	for _, w := range in.waiters {
		if !now.After(w.expires) { // an expired one deliverReplyLocked drops
			verb, _, _ := strings.Cut(strings.ToLower(w.line), " ")
			return verb
		}
	}
	return ""
}

func (in *Instance) deliverReplyLocked(e logparse.Entry) (hidden bool, line string) {
	now := time.Now()
	for len(in.waiters) > 0 {
		w := in.waiters[0]
		if now.After(w.expires) {
			in.waiters = in.waiters[1:]
			close(w.ch)
			continue
		}
		if w.accept != nil && !w.accept(e.Text) {
			return false, ""
		}
		in.waiters = in.waiters[1:]
		w.ch <- e
		close(w.ch)
		return w.internal, w.line
	}
	return false, ""
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

// setRoleLocked gives an online player, by SteamID64 or name, the role a
// command gave them; "" is none.
func (in *Instance) setRoleLocked(who, role string) {
	p := in.players[who]
	for _, x := range in.players {
		if p == nil && x.Name == who {
			p = x
		}
	}
	if role = in.givenRoleLocked(role); p == nil || p.Role == role {
		return
	}
	p.setRole(role)
	in.publishPlayersLocked()
}

// givenRoleLocked is a role as the roster keeps it: "" for the default one,
// which the server names for everyone who was given none.
func (in *Instance) givenRoleLocked(role string) string {
	if role == in.defaultRole {
		return ""
	}
	return role
}

// newPlayerLocked is a player who joined just now, with the role the server
// names for them.
func (in *Instance) newPlayerLocked(id, name, role string) *Player {
	p := &Player{ID: id, Name: name, JoinedAt: time.Now().UnixMilli()}
	p.setRole(in.givenRoleLocked(role))
	return p
}

// setRole gives the player a role as the roster keeps it (givenRoleLocked).
func (p *Player) setRole(role string) { p.Role, p.Admin = role, role == serverconfig.AdminRole }

// joinedLocked runs OnJoin for a player new on the roster.
func (in *Instance) joinedLocked(p Player) {
	if in.OnJoin != nil {
		in.hooks.Go(func() { in.OnJoin(p) })
	}
	in.noticeLocked(Notice{Kind: NoticeJoined, Player: p})
}

func (in *Instance) noticeLocked(n Notice) {
	if in.OnNotice != nil {
		in.OnNotice(n)
	}
}

// goneKeep is how many players who left a run keeps for namesLocked.
const goneKeep = 256

// leftLocked runs OnLeave for a player gone from the roster.
func (in *Instance) leftLocked(l Leave) {
	if len(in.gone) >= goneKeep {
		in.gone = in.gone[1:]
	}
	in.gone = append(in.gone, l.Player)
	if in.OnLeave != nil {
		in.hooks.Go(func() { in.OnLeave(l) })
	}
	in.noticeLocked(Notice{Kind: NoticeLeft, Player: l.Player, Leave: l})
}

// namesLocked tells who the server means by a name in its lines: the
// SteamID64 of the one player by that name on the roster or who left this
// run, whose names its lines still use, or "" for nobody or more than one.
func (in *Instance) namesLocked() func(name string) string {
	ids := make(map[string]string, len(in.players)+len(in.gone))
	add := func(p Player) {
		if id, seen := ids[p.Name]; !seen {
			ids[p.Name] = p.ID
		} else if id != p.ID {
			ids[p.Name] = "" // two players had it
		}
	}
	for _, p := range in.players {
		add(*p)
	}
	for _, p := range in.gone {
		add(p)
	}
	return func(name string) string { return ids[name] }
}

// emptyRosterLocked takes everyone off the roster, each leaving for why.
func (in *Instance) emptyRosterLocked(why string) {
	now := time.Now().UnixMilli()
	for _, p := range in.players {
		in.leftLocked(Leave{Player: *p, At: now, Reason: why})
	}
	in.players = map[string]*Player{}
	in.unmatched = nil
}

// Reconcile replaces the roster with the server's own `players` list. The
// reply is hidden from the console but still lands in ReSkateServer.log, so
// it runs only when the join/leave lines disagree with the server's count.
func (in *Instance) Reconcile() {
	in.mu.Lock()
	in.reconciling = false
	in.mu.Unlock()
	reply, err := in.send(context.Background(), "players", "", true, replyShape(logparse.PlayersHeadRe))
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
		p := in.newPlayerLocked(r.ID, r.Name, r.Role)
		if old, ok := in.players[r.ID]; ok {
			p.JoinedAt = old.JoinedAt
		} else {
			// A join the console showed some other way (its line taken for
			// another kind, say): still a player who was here.
			in.joinedLocked(*p)
		}
		next[r.ID] = p
	}
	// Gone without a leave line of their own: one that named two players
	// was one of these.
	now := time.Now().UnixMilli()
	for id, p := range in.players {
		if next[id] != nil {
			continue
		}
		l := Leave{Player: *p, At: now, Reason: LeftNotOn}
		if i := slices.IndexFunc(in.unmatched, func(u Leave) bool { return u.Name == p.Name }); i >= 0 {
			l.At, l.Reason = in.unmatched[i].At, in.unmatched[i].Reason
			in.unmatched = slices.Delete(in.unmatched, i, i+1)
		}
		in.leftLocked(l)
	}
	in.players, in.unmatched = next, nil
	in.publishPlayersLocked()
}

// startupLineLocked reports whether a line can be the server's own at this
// point. It prints that it is up while starting, and its Steam ID and join
// code straight after (Server/main.cpp); a player can't be on by then. From
// 2.0.6 it also prints what words answers then, once: after that, such a line
// is the reply to a words command.
func (in *Instance) startupLineLocked(e logparse.Entry) bool {
	early := in.state == Starting || in.state == Running && time.Since(in.readyAt) < 10*time.Second
	switch e.Kind {
	case logparse.KindReady:
		return in.state == Starting
	case logparse.KindSteam, logparse.KindJoinCode:
		return early
	case logparse.KindStartup:
		if !strings.HasPrefix(e.Text, logparse.ChatWords) {
			return true
		}
		first := early && !in.wordsShown
		in.wordsShown = true
		return first
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
