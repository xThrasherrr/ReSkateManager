package luavm

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// testPrelude is a small API: on(fn) keeps fn, log(...) records a line.
const testPrelude = `
local call, loadsource = ...
function on(fn) return call("on", fn) end
function log(...) local t = {} for i = 1, select("#", ...) do t[i] = tostring((select(i, ...))) end return call("log", table.concat(t, " ")) end
function echo(...) return call("echo", ...) end
function source(name) local src = call("source", name) return loadsource(src, "=" .. name) end
function compile(src) return loadsource(src, "=x") end
`

type harness struct {
	vm   *VM
	logs []string
	fns  []Ref
}

var (
	rtOnce sync.Once
	rt     *Runtime
)

func newHarness(t testing.TB, timeout time.Duration) *harness {
	t.Helper()
	rtOnce.Do(func() {
		var err error
		if rt, err = NewRuntime(context.Background()); err != nil {
			panic(err)
		}
	})
	h := &harness{}
	vm, err := rt.New(context.Background(), testPrelude, func(name string, args []any) ([]any, error) {
		switch name {
		case "on":
			fn, ok := args[0].(Ref)
			if !ok {
				return nil, errors.New("bad argument #1 to 'on' (function expected)")
			}
			h.vm.Keep(fn)
			h.fns = append(h.fns, fn)
			return nil, nil
		case "log":
			h.logs = append(h.logs, args[0].(string))
			return nil, nil
		case "echo":
			return args, nil
		case "source":
			return []any{"return " + args[0].(string)}, nil
		}
		return nil, fmt.Errorf("no function %s", name)
	}, timeout)
	if err != nil {
		t.Fatal(err)
	}
	h.vm = vm
	t.Cleanup(vm.Close)
	return h
}

func (h *harness) run(t *testing.T, src string) {
	t.Helper()
	if err := h.vm.Run("=main.lua", src); err != nil {
		t.Fatalf("run: %v", err)
	}
}

func TestRunAndCall(t *testing.T) {
	h := newHarness(t, time.Second)
	h.run(t, `
n = 0
on(function(e) n = n + e.points log("got", e.name, e.points, n, math.type(e.points), e.tags[2]) end)
log(_VERSION, 7 // 2, 7 / 2, ("%q"):format("a\nb"))
`)
	ev := map[string]any{"name": "Zee", "points": int64(12400), "tags": []any{"jam", "won"}}
	for range 2 {
		if err := h.vm.Call(h.fns[0], ev); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"Lua 5.4 3 3.5 \"a\\\nb\"", "got Zee 12400 12400 integer won", "got Zee 12400 24800 integer won"}
	if strings.Join(h.logs, "|") != strings.Join(want, "|") {
		t.Fatalf("logs = %q, want %q", h.logs, want)
	}
}

func TestValuesRoundTrip(t *testing.T) {
	h := newHarness(t, time.Second)
	h.run(t, `
local a, b, c, d, e, f = echo(nil, true, 42, 2.5, "x\0y", {1, 2, k = {deep = "yes"}})
log(a, b, c, d, #e, f[2], f.k.deep, math.type(c), math.type(d))
log(pcall(function() echo(coroutine.create(print)) end))
local t = {} t.self = t
log(pcall(function() echo(t) end))
log(pcall(function() echo({[{}] = 1}) end))
`)
	want := []string{
		"nil true 42 2.5 3 2 yes integer float",
		"false main.lua:4: a thread can't be passed to the manager",
		"false main.lua:6: table nested too deeply to pass to the manager (a table inside itself?)",
		"false main.lua:7: a table key that is a table can't be passed to the manager",
	}
	if strings.Join(h.logs, "|") != strings.Join(want, "|") {
		t.Fatalf("logs:\n%s\nwant:\n%s", strings.Join(h.logs, "\n"), strings.Join(want, "\n"))
	}
}

func TestErrors(t *testing.T) {
	h := newHarness(t, time.Second)
	err := h.vm.Run("=main.lua", "local x = nil\nx.y = 1")
	var le *Error
	if !errors.As(err, &le) || !strings.HasPrefix(le.Message, "main.lua:2: attempt to index a nil value (local 'x')") || !strings.Contains(le.Message, "stack traceback") {
		t.Fatalf("err = %v", err)
	}
	if err := h.vm.Run("=main.lua", "on(1)"); err == nil || !strings.Contains(err.Error(), "main.lua:1: bad argument #1 to 'on' (function expected)") {
		t.Fatalf("err = %v", err)
	}
	if err := h.vm.Run("=main.lua", "error({code = 7})"); err == nil || !strings.HasPrefix(err.Error(), "table: ") {
		t.Fatalf("err = %v", err)
	}
	if err := h.vm.Run("=main.lua", "syntax error here"); err == nil || !strings.Contains(err.Error(), "main.lua:1:") {
		t.Fatalf("err = %v", err)
	}
	h.run(t, `log("still fine")`) // a Lua error leaves the VM usable
	if h.logs[0] != "still fine" {
		t.Fatal(h.logs)
	}
}

// TestSandbox checks that plugins get the safe libraries and nothing that
// reaches outside: no files, no processes, no loading code or bytecode.
func TestSandbox(t *testing.T) {
	h := newHarness(t, time.Second)
	h.run(t, `
local found = {}
for _, name in ipairs({"os", "io", "debug", "package", "require", "dofile", "loadfile", "load", "loadstring", "module"}) do
  if _G[name] ~= nil then found[#found+1] = name end
end
log("blocked:", #found, table.concat(found, ","))
log("have:", type(string), type(table), type(math), type(utf8), type(coroutine), type(pcall), type(collectgarbage))
local f = source("1 + 1")
log("source:", f())
local f, err = compile(string.dump(function() end))
log("bytecode:", f == nil and err:match("attempt to load a binary chunk") ~= nil)
`)
	want := "blocked: 0 |have: table table table table table function function|source: 2|bytecode: true"
	if got := strings.Join(h.logs, "|"); got != want {
		t.Fatalf("got %q", got)
	}
}

// TestRunaway: each of these runs until stopped, and the stop gets through
// any pcall, coroutine or library call the loop sits in.
func TestRunaway(t *testing.T) {
	for name, src := range map[string]string{
		"loop":      `while true do end`,
		"pcall":     `while true do pcall(function() while true do end end) end`,
		"xpcall":    `xpcall(function() while true do end end, function() while true do end end)`,
		"coroutine": `while true do pcall(coroutine.wrap(function() while true do end end)) end`,
		"tailcall":  `local function f() return f() end f()`,
		"pattern":   `local s = string.rep("a", 3000) s:find(".-.-.-.-b")`,
		"sort":      `local t = {} for i = 1, 200 do t[i] = i end table.sort(t, function() while true do end end)`,
		"gsub":      `local s = string.rep("a", 1000) s:gsub(".-.-.-b", "")`,
		"close":     `do local x <close> = setmetatable({}, {__close = function() while true do end end}) end`,
		"gc":        `setmetatable({}, {__gc = function() while true do end end}) collectgarbage() while true do end`,
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, 100*time.Millisecond)
			start := time.Now()
			err := h.vm.Run("=main.lua", src)
			took := time.Since(start)
			if !errors.Is(err, ErrTimeout) {
				t.Fatalf("err = %v", err)
			}
			if took > 2*time.Second {
				t.Fatalf("took %s to stop", took)
			}
			if !errors.Is(h.vm.Dead(), ErrTimeout) {
				t.Fatalf("dead = %v", h.vm.Dead())
			}
			if err := h.vm.Run("=main.lua", `log(1)`); !errors.Is(err, ErrTimeout) {
				t.Fatalf("after: %v", err)
			}
		})
	}
}

// TestMemory: a plugin can't take more than MemoryLimit, however it tries,
// and running out is an error in the plugin, which goes on.
func TestMemory(t *testing.T) {
	for name, src := range map[string]string{
		"double": `local s = "x" while true do s = s .. s end`,
		"rep":    `local s = string.rep("x", 1 << 33)`,
		"table":  `local t = {} for i = 1, math.maxinteger do t[i] = i end`,
		"loop":   `local t = {} while true do t[#t+1] = string.rep("x", 1 << 20) .. #t end`,
		"concat": `local s = string.rep("x", 1 << 20) local t = {} for i = 1, 200 do t[i] = s end table.concat(t)`,
		"stack":  `local function f() return 1 + f() end f()`,
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, 10*time.Second)
			err := h.vm.Run("=main.lua", src)
			var le *Error
			if !errors.As(err, &le) {
				t.Fatalf("err = %v", err)
			}
			if !strings.Contains(le.Message, "not enough memory") && !strings.Contains(le.Message, "too large") && !strings.Contains(le.Message, "stack overflow") {
				t.Fatalf("err = %v", le.Message)
			}
			h.run(t, `log("fine")`)
		})
	}
	t.Run("held", func(t *testing.T) { // memory kept across calls counts too
		h := newHarness(t, 10*time.Second)
		h.run(t, `big = {} on(function() for i = 1, 8 do big[#big+1] = string.rep("z", 1 << 20) .. #big end end)`)
		var err error
		calls := 0
		for ; calls < 100 && err == nil; calls++ {
			err = h.vm.Call(h.fns[0])
		}
		if err == nil || !strings.Contains(err.Error(), "not enough memory") {
			t.Fatalf("after %d calls: %v", calls, err)
		}
		if m := h.vm.Memory(); m > MemoryLimit {
			t.Fatalf("memory %d", m)
		}
	})
}

func TestStop(t *testing.T) {
	h := newHarness(t, time.Minute)
	go func() {
		time.Sleep(50 * time.Millisecond)
		h.vm.Stop()
	}()
	if err := h.vm.Run("=main.lua", `while true do end`); !errors.Is(err, ErrStopped) {
		t.Fatalf("err = %v", err)
	}
	h2 := newHarness(t, time.Minute)
	h2.vm.Stop() // nothing running: nothing happens
	h2.run(t, `log("ran")`)
}

// TestRefs: a function the host didn't keep is released after the call; one
// it kept stays until released.
func TestRefs(t *testing.T) {
	h := newHarness(t, time.Second)
	h.run(t, `
f = function() log("kept") end
on(f)
for i = 1, 1000 do echo(function() end) end
`)
	if err := h.vm.Call(h.fns[0]); err != nil {
		t.Fatal(err)
	}
	h.vm.Release(h.fns[0])
	if err := h.vm.Call(h.fns[0]); err == nil { // the registry slot holds no function now
		t.Fatal("called a released function")
	}
	if strings.Join(h.logs, "|") != "kept" {
		t.Fatal(h.logs)
	}
}

// TestFinalizerCalls: a finalizer may call the host while another call's
// results are being read.
func TestFinalizerCalls(t *testing.T) {
	h := newHarness(t, 10*time.Second)
	h.run(t, `
local count = 0
for i = 1, 2000 do
  setmetatable({}, {__gc = function() count = count + 1 echo("from gc", i) end})
  local a, b = echo(string.rep("v", 100), {list = {1, 2, 3}, i = i})
  assert(#a == 100 and b.i == i and b.list[3] == 3)
end
collectgarbage()
log("finalized", count > 0)
`)
	if strings.Join(h.logs, "|") != "finalized true" {
		t.Fatal(h.logs)
	}
}

// TestNoGoroutinesLeft: VMs, coroutines left suspended in them included,
// leave nothing running once closed.
func TestNoGoroutinesLeft(t *testing.T) {
	newHarness(t, time.Second) // the runtime itself
	before := runtime.NumGoroutine()
	for range 50 {
		h := newHarness(t, time.Second)
		h.run(t, `co = coroutine.create(function() pcall(coroutine.yield) end) coroutine.resume(co)`)
		h.vm.Close()
	}
	time.Sleep(50 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before {
		t.Fatalf("goroutines %d before, %d after", before, after)
	}
}

func TestParallel(t *testing.T) {
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			h := newHarness(t, 5*time.Second)
			if err := h.vm.Run("=main.lua", fmt.Sprintf(`local n = 0 for j = 1, 100000 do n = n + j end log(n + %d)`, i)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
}

func FuzzDecodeList(f *testing.F) {
	for _, vals := range [][]any{{nil, true, int64(1), 2.5, "s", Ref(3)}, {[]any{"a", map[string]any{"k": []any{int64(1)}}}}} {
		b, _ := encodeList(vals)
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		vals, _, err := decodeList(b)
		if err != nil {
			return
		}
		again, err := encodeList(vals)
		if err != nil {
			t.Fatal(err)
		}
		if string(again) != string(b) {
			t.Fatalf("round trip changed %x to %x", b, again)
		}
	})
}

func BenchmarkCall(b *testing.B) {
	h := newHarness(b, time.Second)
	if err := h.vm.Run("=main.lua", `n = 0 on(function(e) n = n + 1 names = e.name end)`); err != nil {
		b.Fatal(err)
	}
	ev := map[string]any{"server": "skate-3", "id": "76561198000000000", "name": "Skater"}
	for b.Loop() {
		if err := h.vm.Call(h.fns[0], ev); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkLua is Lua's own speed here: recursion, and string and table work,
// with a C call in every round.
func BenchmarkLua(b *testing.B) {
	h := newHarness(b, time.Minute)
	if err := h.vm.Run("=main.lua", `
on(function()
  local function fib(n) if n < 2 then return n end return fib(n-1) + fib(n-2) end
  local t = {}
  for i = 1, 20000 do t[#t+1] = string.format("%d:%s", i, ("x"):rep(i % 7)) end
  return fib(24) + #table.concat(t)
end)`); err != nil {
		b.Fatal(err)
	}
	for b.Loop() {
		if err := h.vm.Call(h.fns[0]); err != nil {
			b.Fatal(err)
		}
	}
}
