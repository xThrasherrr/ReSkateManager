package plugins

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"github.com/xThrasherrr/ReSkateManager/internal/logparse"
	"github.com/xThrasherrr/ReSkateManager/internal/plugins/luavm"
)

// What a plugin may send ReSkate plugins: a queue and rate of their own, so
// its messages don't wait behind its server.* commands, nor those behind
// them. Each is still a console line, sent one at a time with the panel's.
const (
	tellRate  = 10.0 // messages a second, past tellBurst at once
	tellBurst = 20.0
	tellQueue = 64   // messages waiting to be sent
	maxTell   = 4096 // bytes of the line, ReSkate's console_max_command_bytes
)

// reskateCall answers reskate.send(server, plugin, data [, fn]): data goes
// as JSON in base64 to the ReSkate plugin's console command "rsm <id>".
func (r *runner) reskateCall(name string, args []any) ([]any, error) {
	if name != "reskate.send" {
		return nil, fmt.Errorf("no function %s", name)
	}
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
	in, err := r.server(name, 1, arg(0))
	if err != nil {
		return nil, err
	}
	plugin, ok := arg(1).(string)
	if !ok || !logparse.BridgeIDRe.MatchString(plugin) {
		return nil, badArg(2, name, "ReSkate plugin's id", arg(1))
	}
	if arg(2) == nil {
		return nil, fmt.Errorf("bad argument #3 to '%s' (nothing to send)", name)
	}
	v, err := jsonValue(arg(2), 0)
	if err != nil {
		return nil, fmt.Errorf("bad argument #3 to '%s' (%v)", name, err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("bad argument #3 to '%s' (%v)", name, err)
	}
	// What base64 of the JSON fits on the line after "rsm <id> ".
	if most := (maxTell - len("rsm "+plugin+" ")) / 4 * 3; len(b) > most {
		return nil, fmt.Errorf("bad argument #3 to '%s' (%d bytes as JSON, and a message to %s holds %d)", name, len(b), plugin, most)
	}
	if err := r.need(name, ReSkateSend, in.ID()); err != nil {
		return nil, err
	}
	a.gen, a.server, a.in, a.call, a.tell, a.line = r.gen, in.ID(), in, name, plugin, base64.StdEncoding.EncodeToString(b)
	select {
	case r.tells <- a:
	default:
		r.p.mu.Lock()
		r.p.stats.Unsent++
		r.p.mu.Unlock()
		return []any{false, fmt.Sprintf("%d messages are waiting to be sent already", tellQueue)}, nil
	}
	if a.hasFn {
		r.vm.Keep(a.fn)
	}
	return []any{true}, nil
}

// jsonValue is a Lua value as JSON has it: a table whose keys are 1 to n is
// a list, and any other an object, which takes string keys only.
func jsonValue(v any, depth int) (any, error) {
	switch v := v.(type) {
	case nil, bool, int64, string:
		return v, nil
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, fmt.Errorf("JSON has no %v", v)
		}
		return v, nil
	case *luavm.Table:
		if depth++; depth > logparse.BridgeDepth {
			return nil, fmt.Errorf("tables go %d deep at most", logparse.BridgeDepth)
		}
		list := make([]any, len(v.Keys))
		for i, k := range v.Keys {
			n, ok := k.(int64)
			if !ok || n < 1 || n > int64(len(list)) {
				list = nil
				break
			}
			list[n-1] = v.Vals[i]
		}
		if len(list) > 0 {
			for i, x := range list {
				var err error
				if list[i], err = jsonValue(x, depth); err != nil {
					return nil, err
				}
			}
			return list, nil
		}
		obj := make(map[string]any, len(v.Keys))
		for i, k := range v.Keys {
			s, ok := k.(string)
			if !ok {
				return nil, errors.New("a table is a list, 1 to n, or has string keys")
			}
			x, err := jsonValue(v.Vals[i], depth)
			if err != nil {
				return nil, err
			}
			obj[s] = x
		}
		return obj, nil
	}
	return nil, fmt.Errorf("can't send a %s", luavm.TypeName(v))
}
