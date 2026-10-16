package luavm

import (
	"errors"
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"
)

// TestStopStress runs runaway calls over and over, to catch a stop that's
// lost. STRESS sets how many; it's skipped without.
func TestStopStress(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("STRESS"))
	if n == 0 {
		t.Skip("set STRESS")
	}
	for i := range n {
		h := newHarness(t, 10*time.Millisecond)
		h.run(t, `on(function() while true do pcall(function() while true do end end) end end)`)
		done := make(chan error, 1)
		go func() { done <- h.vm.Call(h.fns[0]) }()
		select {
		case err := <-done:
			if !errors.Is(err, ErrTimeout) {
				t.Fatalf("run %d: %v", i, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("run %d still running", i)
		}
		h.vm.Close()
	}
}

// TestStopDuringGC: a call that runs away is still stopped while the
// garbage collector needs every goroutine to pause, which running
// WebAssembly can't until it calls into Go.
func TestStopDuringGC(t *testing.T) {
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				runtime.GC()
			}
		}
	}()
	for i := range 20 {
		h := newHarness(t, 20*time.Millisecond)
		h.run(t, `on(function() while true do end end)`)
		done := make(chan error, 1)
		go func() { done <- h.vm.Call(h.fns[0]) }()
		select {
		case err := <-done:
			if !errors.Is(err, ErrTimeout) {
				t.Fatalf("run %d: %v", i, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("run %d still running", i)
		}
		h.vm.Close()
	}
}
