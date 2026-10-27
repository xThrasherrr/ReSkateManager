package plugins

import (
	"errors"
	"fmt"

	"github.com/xThrasherrr/ReSkateManager/internal/plugins/luavm"
)

// Plugins call what other plugins expose. A call is a copy of its args, put
// on the other plugin's queue as an event is; the answer, a copy too, comes
// back on the caller's as a command's reply does. Neither waits on the other.

const (
	maxCalls   = 64 // calls a plugin may have waiting for answers
	maxExposed = 64 // bytes of a name a plugin exposes
)

// called is a call waiting on the plugin it's to.
type called struct {
	caller *runner
	name   string
	args   any    // as jsonValue copied it
	res    result // the caller's fn and gen, and the answer once given
}

// reply sends the caller the answer, or err, on a goroutine of its own: the
// caller's queue may be full, and the caller may be busy calling this plugin.
func (c *called) reply(err error) {
	c.res.err = err
	go func() {
		defer c.caller.calls.Add(-1)
		select {
		case c.caller.jobs <- job{result: &c.res}:
		case <-c.caller.quit:
		}
	}()
}

// expose answers expose(name, fn): fn is the prelude's, which runs the
// plugin's own and hands back what it returned (answerCall).
func (r *runner) expose(v, wrapped any) error {
	name, ok := v.(string)
	if !ok || name == "" || len(name) > maxExposed {
		return badArg(1, "expose", fmt.Sprintf("name of 1 to %d bytes", maxExposed), v)
	}
	if len(r.handlers["call:"+name]) > 0 {
		return fmt.Errorf("bad argument #1 to 'expose' (it exposes %s already)", name)
	}
	fn, ok := wrapped.(luavm.Ref)
	if !ok {
		return badArg(2, "expose", "function", wrapped)
	}
	return r.addHandler("call:"+name, fn)
}

// callCall answers call(plugin, name, args [, fn]).
func (r *runner) callCall(args []any) ([]any, error) {
	var a action
	if n := len(args); n > 3 {
		if fn, ok := args[n-1].(luavm.Ref); ok {
			a.fn, a.hasFn = fn, true
			args = args[:n-1]
		}
	}
	arg := func(i int) any {
		if i < len(args) {
			return args[i]
		}
		return nil
	}
	plugin, ok := arg(0).(string)
	if !ok {
		return nil, badArg(1, "call", "plugin ID", arg(0))
	}
	name, ok := arg(1).(string)
	if !ok {
		return nil, badArg(2, "call", "string", arg(1))
	}
	v, err := jsonValue(arg(2), 0)
	if err != nil {
		return nil, fmt.Errorf("bad argument #3 to 'call' (%v)", err)
	}
	if r.calls.Add(1) > maxCalls {
		r.calls.Add(-1)
		return []any{false, fmt.Sprintf("%d calls are waiting for answers already", maxCalls)}, nil
	}
	a.gen, a.call = r.gen, "call "+plugin+"."+name
	c := &called{caller: r, name: name, args: v, res: result{action: a}}
	if why := r.m.put(plugin, c); why != "" {
		r.calls.Add(-1)
		return []any{false, why}, nil
	}
	if a.hasFn {
		r.vm.Keep(a.fn)
	}
	return []any{true}, nil
}

// put queues a call for the plugin id, or says why it can't: under runMu, so
// a plugin that stops, once unlisted, gets no more (refuseCalls).
func (m *Manager) put(id string, c *called) string {
	m.runMu.Lock()
	defer m.runMu.Unlock()
	if cur := m.running.Load(); cur != nil {
		for _, r := range *cur {
			if r.p.ID != id {
				continue
			}
			if !r.wants("call:" + c.name) {
				return fmt.Sprintf("%s doesn't expose %s", id, c.name)
			}
			select {
			case r.jobs <- job{call: c}:
				return ""
			default:
				r.p.mu.Lock()
				r.p.stats.Dropped++
				r.p.mu.Unlock()
				return id + " is too busy"
			}
		}
	}
	return id + " isn't running"
}

// serve runs a call made of what the plugin exposed, and answers it.
func (r *runner) serve(c *called) bool {
	fns := r.handlers["call:"+c.name]
	if len(fns) == 0 { // gone with a fresh start since
		c.reply(fmt.Errorf("%s doesn't expose %s", r.p.ID, c.name))
		return true
	}
	r.p.mu.Lock()
	r.p.stats.Events++
	r.p.mu.Unlock()
	e := map[string]any{"from": c.caller.p.ID}
	if c.args != nil {
		e["args"] = c.args
	}
	r.serving, r.given = c, false
	ok := r.call("call "+c.name, fns[0], e)
	r.serving = nil
	switch {
	case r.given:
		c.reply(nil)
	case r.vm == nil:
		c.reply(fmt.Errorf("%s stopped", r.p.ID))
	default:
		c.reply(fmt.Errorf("%s's %s failed: its log says why", r.p.ID, c.name))
	}
	return ok || r.vm != nil
}

// answerCall takes what the plugin's exposed function returned, from the
// prelude, for the call it's serving.
func (r *runner) answerCall(v any) error {
	if r.serving == nil || r.given {
		return errors.New("no call to answer")
	}
	v, err := jsonValue(v, 0)
	if err != nil {
		return fmt.Errorf("its answer to %s can't go back: %v", r.serving.caller.p.ID, err)
	}
	r.serving.res.reply, r.given = v, true
	return nil
}

// refuseCalls answers the calls still queued for a plugin that stopped, once
// it's unlisted and no more can come.
func (r *runner) refuseCalls() {
	for {
		select {
		case j := <-r.jobs:
			if j.call != nil {
				j.call.reply(fmt.Errorf("%s stopped", r.p.ID))
			}
		default:
			return
		}
	}
}
