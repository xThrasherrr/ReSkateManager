package plugins

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/plugins/luavm"
)

// The limits a plugin runs under.
var (
	// CallTime is the longest one call into a plugin may run: a handler, a
	// timer, or its main.lua.
	CallTime = 100 * time.Millisecond
	// A plugin that runs past CallTime starts afresh; this many times within
	// overrunWindow and it's stopped.
	overrunLimit  = 3
	overrunWindow = 10 * time.Minute
	// A plugin whose calls fail this many times within failWindow is stopped.
	failLimit  = 20
	failWindow = time.Minute
)

const (
	queueSize   = 256 // events and timers waiting for a plugin
	maxHandlers = 256
	maxTimers   = 256
	minEvery    = 100 * time.Millisecond
	maxDelay    = 7 * 24 * time.Hour
	maxLine     = 2000 // bytes of one log line
	logBurst    = 100  // log lines a plugin may write at once, then logRate a second
	logRate     = 20
)

type job struct {
	event   string
	payload map[string]any
	timer   int64
	result  *result // what came of a command it sent
}

type timer struct {
	fn        luavm.Ref
	every     time.Duration // 0 for after
	t         atomic.Pointer[time.Timer]
	queued    atomic.Bool // a firing waits in the queue; the next is skipped
	cancelled atomic.Bool
}

func (t *timer) cancel() {
	t.cancelled.Store(true)
	if tm := t.t.Load(); tm != nil {
		tm.Stop()
	}
}

// runner runs one plugin on its own goroutine, which alone touches its VM,
// handlers and timers. The servers' events reach it through jobs, which never
// blocks the server: a full queue drops the event and counts it. Its
// commands go out on a goroutine of their own (send).
type runner struct {
	m       *Manager
	p       *Plugin
	man     Manifest
	rt      *luavm.Runtime
	servers map[string]bool // nil for all of them
	jobs    chan job
	acts    chan action
	ctx     context.Context // done once it stops
	cancel  context.CancelFunc
	quit    <-chan struct{} // ctx.Done()
	done    chan struct{}
	sent    chan struct{}                   // closed once send has returned
	want    atomic.Pointer[map[string]bool] // the events it has handlers for

	cards        sync.WaitGroup     // the cards waiting their turn (uiCall)
	cardsWaiting atomic.Int32       // how many
	cardCtx      context.Context    // done once its Lua starts afresh, dropping them
	cardCancel   context.CancelFunc // ends cardCtx

	vmMu sync.Mutex // guards vm, which stop reads from another goroutine
	vm   *luavm.VM

	handlers  map[string][]luavm.Ref
	timers    map[int64]*timer
	nextTimer int64
	gen       uint64 // counts fresh starts, so a command's answer finds the Lua that sent it
	overruns  []time.Time
	fails     []time.Time
	tokens    float64 // for the log's rate
	tokensAt  time.Time
	unlogged  int // lines dropped since the log last had room
}

func newRunner(m *Manager, p *Plugin, man Manifest, rt *luavm.Runtime) *runner {
	r := &runner{m: m, p: p, man: man, rt: rt, jobs: make(chan job, queueSize), acts: make(chan action, actionQueue), done: make(chan struct{}),
		sent: make(chan struct{}), tokens: logBurst, tokensAt: time.Now()}
	r.ctx, r.cancel = context.WithCancel(context.Background())
	r.quit = r.ctx.Done()
	r.cardCtx, r.cardCancel = context.WithCancel(r.ctx)
	if len(man.Servers) > 0 {
		r.servers = map[string]bool{}
		for _, s := range man.Servers {
			r.servers[s] = true
		}
	}
	return r
}

func (r *runner) onServer(id string) bool { return r.servers == nil || r.servers[id] }

func (r *runner) wants(event string) bool {
	w := r.want.Load()
	return w != nil && (*w)[event]
}

func (r *runner) enqueue(j job) {
	select {
	case r.jobs <- j:
	default:
		r.p.mu.Lock()
		r.p.stats.Dropped++
		r.p.mu.Unlock()
	}
}

func (r *runner) ended() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

// stop ends the runner, breaking off a call that runs, and waits for it.
func (r *runner) stop() {
	r.cancel()
	r.vmMu.Lock()
	if r.vm != nil {
		r.vm.Stop()
	}
	r.vmMu.Unlock()
	<-r.done
}

func (r *runner) loop() {
	defer close(r.done)
	defer func() { <-r.sent }()
	defer r.cards.Wait()
	defer r.cancel() // ends send and the cards waiting when the plugin ends on its own
	defer r.m.unlist(r)
	defer r.reset()
	go r.send()
	if err := r.m.data.load(r.p.ID); err != nil {
		r.fail(Failed, "Its store didn't load: "+err.Error())
		return
	}
	if !r.boot() {
		return
	}
	for {
		select {
		case <-r.quit:
			return
		case j := <-r.jobs:
			if !r.handle(j) {
				return
			}
		}
	}
}

// boot starts a fresh Lua and runs main.lua in it. When it can't, the
// plugin is left with no Lua.
func (r *runner) boot() bool {
	if r.start() {
		return true
	}
	r.dropVM()
	return false
}

func (r *runner) start() bool {
	r.p.setState(Starting, "")
	vm, err := r.rt.New(context.Background(), prelude, r.answer, CallTime)
	if err != nil {
		r.fail(Failed, "Lua didn't start: "+err.Error())
		return false
	}
	r.vmMu.Lock()
	r.vm = vm
	r.vmMu.Unlock()
	select {
	case <-r.quit: // stopped while it started: stop doesn't reach a VM it can't see
		return false
	default:
	}
	src, err := readSource(r.p.dir, mainFile)
	if err == nil {
		start := time.Now()
		err = vm.Run("="+mainFile, src)
		r.took(time.Since(start))
	}
	switch {
	case err == nil:
	case errors.Is(err, luavm.ErrStopped):
		return false
	case errors.Is(err, luavm.ErrTimeout):
		r.fail(Failed, fmt.Sprintf("Its %s ran past %s.", mainFile, CallTime))
		return false
	default:
		r.fail(Failed, "Its "+mainFile+" failed: "+err.Error())
		return false
	}
	r.p.setState(Running, "")
	return true
}

// reset ends the plugin's Lua: its timers stop, its handlers go, and so do
// the commands it hasn't sent and the cards still waiting.
func (r *runner) reset() {
	r.cardCancel()
	r.cardCtx, r.cardCancel = context.WithCancel(r.ctx)
	for _, t := range r.timers {
		t.cancel()
	}
	r.timers, r.handlers = nil, nil
	r.want.Store(nil)
	r.gen++
	for len(r.acts) > 0 {
		select {
		case <-r.acts:
		default:
		}
	}
	r.vmMu.Lock()
	if r.vm != nil {
		r.vm.Close()
		r.vm = nil
	}
	r.vmMu.Unlock()
	r.p.mu.Lock()
	r.p.stats.Timers, r.p.stats.Handlers, r.p.stats.Memory = 0, nil, 0
	r.p.mu.Unlock()
}

// handle runs one job, and reports whether the plugin keeps running.
func (r *runner) handle(j job) bool {
	if j.result != nil {
		return r.answered(j.result)
	}
	if j.event != "" {
		r.p.mu.Lock()
		r.p.stats.Events++
		r.p.mu.Unlock()
		for _, fn := range slices.Clone(r.handlers[j.event]) {
			if !r.call(j.event+" handler", fn, j.payload) {
				return r.vm != nil
			}
		}
		return true
	}
	t, ok := r.timers[j.timer]
	if !ok { // cancelled since
		return true
	}
	t.queued.Store(false)
	if t.every == 0 {
		delete(r.timers, j.timer)
	}
	vm := r.vm
	ok = r.call("timer", t.fn)
	if t.every == 0 && r.vm == vm && vm != nil { // not after a fresh start: its refs are its own
		vm.Release(t.fn)
	}
	r.countTimers()
	if !ok {
		return r.vm != nil
	}
	return true
}

// call runs one of the plugin's functions. A failure goes in its log; one
// that ran past its time starts the plugin afresh. It reports whether the
// plugin's Lua carries on as it was.
func (r *runner) call(what string, fn luavm.Ref, args ...any) bool {
	start := time.Now()
	err := r.vm.Call(fn, args...)
	r.took(time.Since(start))
	if err == nil {
		return true
	}
	switch {
	case errors.Is(err, luavm.ErrStopped):
		r.dropVM()
		return false
	case errors.Is(err, luavm.ErrTimeout):
		r.overruns = within(append(r.overruns, time.Now()), overrunWindow)
		if len(r.overruns) >= overrunLimit {
			r.log("error", fmt.Sprintf("Its %s ran past %s.", what, CallTime))
			r.fail(Stopped, fmt.Sprintf("It ran past %s %d times in %s.", CallTime, overrunLimit, duration(overrunWindow)))
			r.dropVM()
			return false
		}
		r.log("error", fmt.Sprintf("Its %s ran past %s; it starts afresh.", what, CallTime))
		r.p.mu.Lock()
		r.p.restarts++
		r.p.mu.Unlock()
		r.reset()
		r.boot() // its handlers are new: the job goes no further
		return false
	case r.vm.Dead() != nil:
		r.fail(Stopped, "Its Lua crashed: "+err.Error())
		r.dropVM()
		return false
	}
	r.log("error", what+": "+err.Error())
	r.p.mu.Lock()
	r.p.stats.Errors++
	r.p.mu.Unlock()
	r.fails = within(append(r.fails, time.Now()), failWindow)
	if len(r.fails) >= failLimit {
		r.fail(Stopped, fmt.Sprintf("It failed %d times in %s.", failLimit, duration(failWindow)))
		r.dropVM()
		return false
	}
	return true
}

// dropVM marks the plugin done, so handle ends the loop.
func (r *runner) dropVM() {
	r.vmMu.Lock()
	if r.vm != nil {
		r.vm.Close()
	}
	r.vm = nil
	r.vmMu.Unlock()
}

func (r *runner) took(d time.Duration) {
	ms := float64(d.Microseconds()) / 1000
	var mem int64
	if r.vm != nil {
		mem = r.vm.Memory()
	}
	r.p.mu.Lock()
	defer r.p.mu.Unlock()
	r.p.stats.Calls++
	r.p.stats.Slowest = max(r.p.stats.Slowest, ms)
	r.p.stats.Memory = mem
}

// fail stops the plugin for a reason its card shows.
func (r *runner) fail(state, reason string) {
	r.p.setState(state, reason)
	r.log("error", reason)
	r.m.Log.Warn("plugin stopped", "plugin", r.p.ID, "why", reason)
}

func within(ts []time.Time, d time.Duration) []time.Time {
	cut := time.Now().Add(-d)
	return slices.DeleteFunc(ts, func(t time.Time) bool { return t.Before(cut) })
}

func duration(d time.Duration) string {
	if d >= time.Minute && d%time.Minute == 0 {
		if d == time.Minute {
			return "a minute"
		}
		return fmt.Sprintf("%d minutes", d/time.Minute)
	}
	return d.String()
}

// log writes a line to the plugin's card and plugins.log, at a rate it
// can't flood: past logBurst lines at once, logRate a second.
func (r *runner) log(level, text string) {
	now := time.Now()
	r.tokens = min(logBurst, r.tokens+now.Sub(r.tokensAt).Seconds()*logRate)
	r.tokensAt = now
	if r.tokens < 1 && level != "error" {
		r.unlogged++
		return
	}
	r.tokens--
	if r.unlogged > 0 {
		msg := fmt.Sprintf("%d lines weren't logged: too many at once.", r.unlogged)
		r.unlogged = 0
		r.p.line("warn", msg)
		r.m.write(r.p.ID, "warn", msg)
	}
	if len(text) > maxLine {
		text = text[:maxLine] + "…"
	}
	r.p.line(level, text)
	r.m.write(r.p.ID, level, text)
}

func (r *runner) countTimers() {
	n := len(r.timers)
	r.p.mu.Lock()
	r.p.stats.Timers = n
	r.p.mu.Unlock()
}

// ---- the plugin's calls ----

// answer runs the plugin API's calls into the manager, from the prelude.
func (r *runner) answer(name string, args []any) ([]any, error) {
	arg := func(i int) any {
		if i < len(args) {
			return args[i]
		}
		return nil
	}
	switch name {
	case "on":
		ev, ok := arg(0).(string)
		if !ok {
			return nil, badArg(1, "on", "string", arg(0))
		}
		if !knownEvent(ev) {
			return nil, fmt.Errorf("bad argument #1 to 'on' (no event %q)", ev)
		}
		fn, ok := arg(1).(luavm.Ref)
		if !ok {
			return nil, badArg(2, "on", "function", arg(1))
		}
		total := 0
		for _, h := range r.handlers {
			total += len(h)
		}
		if total >= maxHandlers {
			return nil, fmt.Errorf("a plugin may have %d handlers at most", maxHandlers)
		}
		r.vm.Keep(fn)
		if r.handlers == nil {
			r.handlers = map[string][]luavm.Ref{}
		}
		r.handlers[ev] = append(r.handlers[ev], fn)
		want := map[string]bool{}
		names := make([]string, 0, len(r.handlers))
		for k := range r.handlers {
			want[k] = true
			names = append(names, k)
		}
		slices.Sort(names)
		r.want.Store(&want)
		r.p.mu.Lock()
		r.p.stats.Handlers = names
		r.p.mu.Unlock()
		return nil, nil
	case "after", "every":
		secs, ok := number(arg(0))
		if !ok {
			return nil, badArg(1, name, "number", arg(0))
		}
		fn, ok := arg(1).(luavm.Ref)
		if !ok {
			return nil, badArg(2, name, "function", arg(1))
		}
		d := time.Duration(secs * float64(time.Second))
		switch {
		case math.IsNaN(secs) || secs < 0 || d > maxDelay:
			return nil, fmt.Errorf("bad argument #1 to '%s' (seconds must be from 0 to a week)", name)
		case name == "every" && d < minEvery:
			return nil, fmt.Errorf("bad argument #1 to 'every' (at least %g seconds)", minEvery.Seconds())
		case len(r.timers) >= maxTimers:
			return nil, fmt.Errorf("a plugin may have %d timers at most", maxTimers)
		}
		r.vm.Keep(fn)
		r.nextTimer++
		id := r.nextTimer
		t := &timer{fn: fn}
		if name == "every" {
			t.every = d
		}
		tm := time.AfterFunc(maxDelay+time.Hour, func() {
			if t.cancelled.Load() {
				return
			}
			if t.every > 0 {
				t.t.Load().Reset(t.every)
			}
			if !t.queued.Swap(true) {
				r.enqueue(job{timer: id})
			}
		})
		t.t.Store(tm)
		tm.Reset(d) // only now, so it can't fire before it's stored
		if r.timers == nil {
			r.timers = map[int64]*timer{}
		}
		r.timers[id] = t
		r.countTimers()
		return []any{id}, nil
	case "cancel":
		id, ok := arg(0).(int64)
		if !ok {
			return []any{false}, nil
		}
		t, ok := r.timers[id]
		if !ok {
			return []any{false}, nil
		}
		t.cancel()
		delete(r.timers, id)
		r.vm.Release(t.fn)
		r.countTimers()
		return []any{true}, nil
	case "log", "warn":
		text, _ := arg(0).(string)
		level := "info"
		if name == "warn" {
			level = "warn"
		}
		r.log(level, text)
		return nil, nil
	case "source":
		mod, ok := arg(0).(string)
		if !ok {
			return nil, badArg(1, "require", "string", arg(0))
		}
		file, err := moduleFile(mod)
		if err != nil {
			return nil, err
		}
		src, err := readSource(r.p.dir, file)
		if err != nil {
			return nil, err
		}
		return []any{src, file}, nil
	}
	switch name[:max(0, strings.IndexByte(name, '.'))] {
	case "server":
		return r.serverCall(name, args)
	case "store", "playtime", "stats":
		return r.dataCall(name, args)
	case "ui":
		return r.uiCall(name, args)
	}
	return nil, fmt.Errorf("no function %s", name)
}

func number(v any) (float64, bool) {
	switch v := v.(type) {
	case int64:
		return float64(v), true
	case float64:
		return v, true
	}
	return 0, false
}

func badArg(n int, fn, want string, got any) error {
	return fmt.Errorf("bad argument #%d to '%s' (%s expected, got %s)", n, fn, want, luavm.TypeName(got))
}
