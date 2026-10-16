// Package luavm runs Lua 5.4 for plugins: PUC Lua compiled to WebAssembly
// (wasm/build.sh, which `task lua:wasm` runs) and run by wazero, one module
// instance per plugin. A plugin's Lua reaches nothing but the functions its
// host answers: no files, processes or network. Its memory is capped as a
// whole, and a call that runs too long is ended where it stands, past any
// pcall. Lua calls into Go every so often to ask whether to stop
// (wasm/plhooks.h), which is also when the Go scheduler and garbage collector
// can pause it: running WebAssembly can't be paused otherwise.
//
// Chosen over the pure-Go Lua 5.4 engines, which couldn't stop a pcall
// swallowing their time limit, a string doubling past the memory cap, or a
// pattern match spinning inside Go, and left goroutines behind on reload.
package luavm

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
	"github.com/tetratelabs/wazero/sys"
)

//go:embed lua.wasm
var luaWasm []byte

// MemoryLimit is the most memory one plugin's Lua may use. Past it, Lua
// raises "not enough memory" in the plugin.
const MemoryLimit = 64 << 20

var (
	// ErrTimeout ends a call that ran past its time.
	ErrTimeout = errors.New("ran past its time")
	// ErrStopped ends a call Stop broke off.
	ErrStopped = errors.New("stopped")
)

// Error is an error the plugin's Lua raised, with its traceback.
type Error struct{ Message string }

func (e *Error) Error() string { return e.Message }

// Handler answers the plugin's calls into the host: the function's name and
// its arguments in, its results out. An error is raised in the plugin.
type Handler func(name string, args []any) ([]any, error)

// Runtime holds the compiled Lua module that every VM is an instance of.
type Runtime struct {
	rt  wazero.Runtime
	mod wazero.CompiledModule
}

type vmKey struct{}

// NewRuntime compiles the Lua module.
func NewRuntime(ctx context.Context) (*Runtime, error) {
	cfg := wazero.NewRuntimeConfig().
		WithCoreFeatures(api.CoreFeaturesV2 | experimental.CoreFeaturesExceptionHandling).
		WithMemoryLimitPages(MemoryLimit / 65536)
	rt := wazero.NewRuntimeWithConfig(ctx, cfg)
	r := &Runtime{rt: rt}
	if err := hostModule(ctx, rt); err != nil {
		rt.Close(ctx)
		return nil, err
	}
	if err := noSystem(ctx, rt); err != nil {
		rt.Close(ctx)
		return nil, err
	}
	mod, err := rt.CompileModule(ctx, luaWasm)
	if err != nil {
		rt.Close(ctx)
		return nil, fmt.Errorf("compile Lua: %w", err)
	}
	r.mod = mod
	return r, nil
}

// Close ends every VM and frees the compiled module.
func (r *Runtime) Close(ctx context.Context) error { return r.rt.Close(ctx) }

// VM is one plugin's Lua. Use it from one goroutine at a time; only Stop may
// be called from another.
type VM struct {
	mod     api.Module
	handle  Handler
	timeout time.Duration
	ctx     context.Context // carries the VM to the host functions
	mem     *vmem
	fn      struct{ alloc, free, prelude, run, callref, unref, memory api.Function }

	mu       sync.Mutex // guards running, which Stop reads from another goroutine
	running  bool
	stopping atomic.Bool // Stop was called during the running call
	deadline time.Time   // when the running call is out of time; zero between calls
	why      error       // why ask stopped the running call

	pending []byte // the last host call's results or error, for take
	failure string // what the bridge reported with fail
	dead    error  // set once the module can run nothing more
	release []Ref  // functions passed in host calls that nobody kept
	kept    map[Ref]bool
}

// New starts a VM and runs prelude in it: the host's own Lua, which gets the
// bridge's call and loadsource functions as its arguments and builds the
// plugin's API from them. Each call into the VM may run for timeout at most.
func (r *Runtime) New(ctx context.Context, prelude string, h Handler, timeout time.Duration) (*VM, error) {
	vm := &VM{handle: h, timeout: timeout}
	var memErr error
	alloc := experimental.MemoryAllocatorFunc(func(_, max uint64) experimental.LinearMemory {
		vm.mem, memErr = newVMem(max)
		if memErr != nil {
			return nil
		}
		return vm.mem
	})
	vm.ctx = experimental.WithMemoryAllocator(context.WithValue(context.Background(), vmKey{}, vm), alloc)
	mod, err := r.rt.InstantiateModule(vm.ctx, r.mod, wazero.NewModuleConfig().WithName("").WithStartFunctions("_initialize"))
	if err == nil && vm.mem == nil {
		err = errors.New("its memory wasn't made")
	}
	if err != nil {
		return nil, fmt.Errorf("start Lua: %w", errors.Join(err, memErr))
	}
	vm.mod = mod
	for name, f := range map[string]*api.Function{"pl_alloc": &vm.fn.alloc, "pl_free": &vm.fn.free, "pl_prelude": &vm.fn.prelude,
		"pl_run": &vm.fn.run, "pl_callref": &vm.fn.callref, "pl_unref": &vm.fn.unref, "pl_memory": &vm.fn.memory} {
		if *f = mod.ExportedFunction(name); *f == nil {
			mod.Close(ctx)
			return nil, fmt.Errorf("start Lua: no %s", name)
		}
	}
	res, err := mod.ExportedFunction("pl_init").Call(vm.ctx)
	if err == nil && int32(res[0]) != 0 {
		err = errors.New("no memory for Lua")
	}
	if err != nil {
		mod.Close(ctx)
		return nil, fmt.Errorf("start Lua: %w", err)
	}
	if err := vm.withText(prelude, func(p, n uint64) (uint64, error) { return vm.invoke(vm.fn.prelude, p, n) }); err != nil {
		vm.Close()
		return nil, fmt.Errorf("prelude: %w", err)
	}
	return vm, nil
}

// Run compiles and runs a chunk of the plugin's; name is the chunk's name in
// messages, as "=main.lua".
func (vm *VM) Run(name, src string) error {
	return vm.withText(name, func(np, nn uint64) (uint64, error) {
		var status uint64
		err := vm.withText(src, func(sp, sn uint64) (uint64, error) {
			var err error
			status, err = vm.invoke(vm.fn.run, np, nn, sp, sn)
			return status, err
		})
		return status, err
	})
}

// Call calls a function the plugin handed the host.
func (vm *VM) Call(fn Ref, args ...any) error {
	b, err := encodeList(args)
	if err != nil {
		return err
	}
	return vm.withBytes(b, func(p, n uint64) (uint64, error) { return vm.invoke(vm.fn.callref, uint64(fn), p, n) })
}

// Keep holds on to a function passed in the host call being answered, which
// is released after the call otherwise. Call Release when done with it.
func (vm *VM) Keep(fn Ref) {
	if vm.kept == nil {
		vm.kept = map[Ref]bool{}
	}
	vm.kept[fn] = true
}

// Release lets go of a function the plugin handed the host.
func (vm *VM) Release(fn Ref) {
	if vm.dead == nil {
		vm.fn.unref.Call(vm.ctx, uint64(fn))
	}
}

// Memory is how many bytes the plugin's Lua uses.
func (vm *VM) Memory() int64 {
	if vm.dead != nil {
		return 0
	}
	res, err := vm.fn.memory.Call(vm.ctx)
	if err != nil {
		return 0
	}
	return int64(int32(res[0])) * 1024
}

// Stop breaks off the call running now, if there is one, from any goroutine.
// The VM can run nothing after a call it stopped.
func (vm *VM) Stop() { vm.stop(ErrStopped) }

// Dead is why the VM can run nothing more, or nil.
func (vm *VM) Dead() error { return vm.dead }

// Close frees the VM.
func (vm *VM) Close() {
	if vm.dead == nil {
		vm.dead = ErrStopped
	}
	vm.mod.Close(context.Background())
}

func (vm *VM) stop(why error) {
	vm.mu.Lock()
	defer vm.mu.Unlock()
	if vm.running {
		vm.stopping.Store(true) // the next ask traps
	}
}

// invoke runs one of the bridge's entry points under the time limit, and
// turns a non-zero status into the error the bridge reported.
func (vm *VM) invoke(f api.Function, args ...uint64) (uint64, error) {
	if vm.dead != nil {
		return 0, vm.dead
	}
	vm.mu.Lock()
	vm.running, vm.why, vm.deadline = true, nil, time.Now().Add(vm.timeout)
	vm.stopping.Store(false)
	vm.mu.Unlock()
	res, err := f.Call(vm.ctx, args...)
	vm.mu.Lock()
	vm.running, vm.deadline = false, time.Time{}
	vm.mu.Unlock()
	why := vm.why
	if err != nil {
		if why == nil { // a trap of the module's own, as from a bug in it
			why = fmt.Errorf("Lua crashed: %w", err)
		}
		vm.dead = why
		vm.mod.Close(context.Background())
		return 0, why
	}
	for _, fn := range vm.release {
		vm.fn.unref.Call(vm.ctx, uint64(fn))
	}
	vm.release = vm.release[:0]
	if res[0] != 0 {
		msg := vm.failure
		vm.failure = ""
		return res[0], &Error{Message: msg}
	}
	return 0, nil
}

func (vm *VM) withText(s string, f func(p, n uint64) (uint64, error)) error {
	return vm.withBytes([]byte(s), f)
}

// withBytes copies b into the module's memory for f, and frees it after.
func (vm *VM) withBytes(b []byte, f func(p, n uint64) (uint64, error)) error {
	if vm.dead != nil {
		return vm.dead
	}
	size := max(len(b), 1)
	res, err := vm.fn.alloc.Call(vm.ctx, uint64(size))
	if err != nil || res[0] == 0 {
		return &Error{Message: "not enough memory"}
	}
	p := uint32(res[0])
	vm.mod.Memory().Write(p, b)
	_, err = f(uint64(p), uint64(len(b)))
	if vm.dead == nil {
		vm.fn.free.Call(vm.ctx, uint64(p))
	}
	return err
}

// ---- the functions the bridge imports ----

func hostModule(ctx context.Context, rt wazero.Runtime) error {
	_, err := rt.NewHostModuleBuilder("rsm").
		NewFunctionBuilder().WithFunc(hostCall).Export("call").
		NewFunctionBuilder().WithFunc(hostTake).Export("take").
		NewFunctionBuilder().WithFunc(hostFail).Export("fail").
		NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(hostAsk), nil, []api.ValueType{api.ValueTypeI32}).Export("ask").
		Instantiate(ctx)
	return err
}

// hostAsk answers Lua's ask (wasm/plhooks.h): non-zero when the call must
// stop, as Stop asked or out of time. Lua asks often, so it allocates
// nothing.
func hostAsk(ctx context.Context, m api.Module, stack []uint64) {
	vm := ctx.Value(vmKey{}).(*VM)
	switch {
	case vm.stopping.Load():
		vm.why = ErrStopped
	case !vm.deadline.IsZero() && time.Now().After(vm.deadline):
		vm.why = ErrTimeout
	default:
		stack[0] = 0
		return
	}
	stack[0] = 1
}

func hostCall(ctx context.Context, m api.Module, namePtr, nameLen, argsPtr, argsLen uint32) int32 {
	vm := ctx.Value(vmKey{}).(*VM)
	name, ok1 := m.Memory().Read(namePtr, nameLen)
	raw, ok2 := m.Memory().Read(argsPtr, argsLen)
	var out []byte
	err := errors.New("bad call")
	if ok1 && ok2 {
		var args []any
		var refs []Ref
		args, refs, err = decodeList(raw)
		vm.kept = nil
		if err == nil {
			var results []any
			results, err = vm.answer(string(name), args)
			if err == nil {
				out, err = encodeList(results)
			}
		}
		for _, fn := range refs {
			if !vm.kept[fn] {
				vm.release = append(vm.release, fn)
			}
		}
		vm.kept = nil
	}
	if err != nil {
		vm.pending = []byte(err.Error())
		return -int32(len(vm.pending)) - 1
	}
	vm.pending = out
	return int32(len(out))
}

// answer runs the handler, which must not take the VM down with a panic.
func (vm *VM) answer(name string, args []any) (results []any, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%s: internal error: %v", name, r)
		}
	}()
	return vm.handle(name, args)
}

func hostTake(ctx context.Context, m api.Module, dst uint32) {
	vm := ctx.Value(vmKey{}).(*VM)
	if dst != 0 {
		m.Memory().Write(dst, vm.pending)
	}
	vm.pending = nil
}

func hostFail(ctx context.Context, m api.Module, p, n uint32) {
	vm := ctx.Value(vmKey{}).(*VM)
	b, _ := m.Memory().Read(p, n)
	vm.failure = string(b)
}

// noSystem stands in for WASI: the C library asks for the time and, never
// given any files, finds none. Lua's io and os aren't built in.
func noSystem(ctx context.Context, rt wazero.Runtime) error {
	const ebadf = 8
	i32, i64 := api.ValueTypeI32, api.ValueTypeI64
	b := rt.NewHostModuleBuilder("wasi_snapshot_preview1")
	start := time.Now()
	b.NewFunctionBuilder().WithFunc(func(ctx context.Context, m api.Module, id uint32, _ uint64, at uint32) uint32 {
		t := time.Now().UnixNano()
		if id != 0 { // the monotonic clocks
			t = int64(time.Since(start))
		}
		m.Memory().WriteUint64Le(at, uint64(t))
		return 0
	}).Export("clock_time_get")
	for name, params := range map[string][]api.ValueType{
		"fd_close":            {i32},
		"fd_fdstat_get":       {i32, i32},
		"fd_fdstat_set_flags": {i32, i32},
		"fd_prestat_dir_name": {i32, i32, i32},
		"fd_prestat_get":      {i32, i32},
		"fd_read":             {i32, i32, i32, i32},
		"fd_renumber":         {i32, i32},
		"fd_seek":             {i32, i64, i32, i32},
		"fd_write":            {i32, i32, i32, i32},
		"path_open":           {i32, i32, i32, i32, i32, i64, i64, i32, i32},
	} {
		b.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
			stack[0] = ebadf
		}), params, []api.ValueType{i32}).Export(name)
	}
	b.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
		code := uint32(stack[0])
		m.CloseWithExitCode(ctx, code)
		panic(sys.NewExitError(code))
	}), []api.ValueType{i32}, nil).Export("proc_exit")
	_, err := b.Instantiate(ctx)
	return err
}
