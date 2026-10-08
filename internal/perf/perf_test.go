//go:build windows || linux

package perf

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/xThrasherrr/ReSkateManager/internal/instance"
	"github.com/xThrasherrr/ReSkateManager/internal/store"
)

// The first tick only reads the machine; from the second on, each records it
// with the servers counted.
func TestRecordsHost(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	log := slog.New(slog.DiscardHandler)
	reg := instance.NewRegistry(log)
	reg.Add(instance.Def{ID: "a", Name: "a", Dir: t.TempDir()})
	reg.Add(instance.Def{ID: "b", Name: "b", Dir: t.TempDir()})
	r := &Recorder{Store: st, Reg: reg, Log: log}
	ctx := context.Background()

	r.sample(ctx)
	if h, err := st.LatestHost(ctx); err != nil || h != nil {
		t.Fatalf("first tick stored %+v %v", h, err)
	}
	r.sample(ctx)
	h, err := st.LatestHost(ctx)
	if err != nil || h == nil {
		t.Fatalf("second tick stored nothing: %v", err)
	}
	if h.Servers != 2 || h.Running != 0 || h.ServersMem != 0 || h.MemTotal == 0 || h.MemUsed <= 0 || h.MemUsed > h.MemTotal || h.ManagerMem < 1<<20 {
		t.Errorf("sample %+v", h)
	}
}
