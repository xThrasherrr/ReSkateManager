package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestPerfPointsAndRuns(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	for _, p := range []PerfSample{
		{At: 1000, Run: 900, CPU: 10, Mem: 100, Players: 1},
		{At: 1030, Run: 900, CPU: 30, Mem: 300, Players: 3},
		{At: 1090, Run: 1080, CPU: 50, Mem: 500, Players: 0},
	} {
		if err := s.AddPerfSample(ctx, "a", p); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AddPerfSample(ctx, "b", PerfSample{At: 1000, Run: 1, CPU: 99}); err != nil {
		t.Fatal(err)
	}
	pts, err := s.PerfPoints(ctx, "a", 0, 60)
	if err != nil {
		t.Fatal(err)
	}
	want := []PerfPoint{{At: 960, Run: 900, CPU: 10, CPUMax: 10, Mem: 100, Players: 1}, {At: 1020, Run: 900, CPU: 30, CPUMax: 30, Mem: 300, Players: 3}, {At: 1080, Run: 1080, CPU: 50, CPUMax: 50, Mem: 500}}
	if len(pts) != len(want) {
		t.Fatalf("points %+v", pts)
	}
	for i := range want {
		if pts[i] != want[i] {
			t.Errorf("point %d = %+v, want %+v", i, pts[i], want[i])
		}
	}
	runs, err := s.PerfRuns(ctx, "a", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[0].Run != 1080 || runs[1] != (PerfRun{Run: 900, First: 1000, Last: 1030, CPU: 20, CPUMax: 30, Mem: 200, MemMax: 300, Players: 3}) {
		t.Errorf("runs %+v", runs)
	}
	if err := s.PrunePerf(ctx, 1050); err != nil {
		t.Fatal(err)
	}
	if pts, _ := s.PerfPoints(ctx, "a", 0, 60); len(pts) != 1 {
		t.Errorf("after prune %+v", pts)
	}
}

func TestHostPoints(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if h, err := s.LatestHost(ctx); err != nil || h != nil {
		t.Fatalf("latest of none: %+v %v", h, err)
	}
	for _, h := range []HostSample{
		{At: 1000, CPU: 20, MemTotal: 1000, MemUsed: 400, MemCached: 100, ServersCPU: 10, ServersMem: 200, ManagerMem: 50, Running: 1, Servers: 2, Players: 3},
		{At: 1030, CPU: 40, MemTotal: 1000, MemUsed: 600, MemCached: 300, ServersCPU: 30, ServersMem: 400, ManagerMem: 50, Running: 2, Servers: 2, Players: 1},
		{At: 1050, CPU: 5, MemTotal: 1000, MemUsed: 100, LimitMax: 800, LimitUsed: 90, Servers: 2},
	} {
		if err := s.AddHostSample(ctx, h); err != nil {
			t.Fatal(err)
		}
	}
	pts, err := s.HostPoints(ctx, 0, 60)
	if err != nil {
		t.Fatal(err)
	}
	want := []HostPoint{
		{At: 960, CPU: 20, MemTotal: 1000, MemUsed: 400, MemCached: 100, ServersCPU: 10, ServersMem: 200, ManagerMem: 50, Running: 1, Servers: 2, Players: 3, CPUMax: 20, Samples: 1},
		{At: 1020, CPU: 22.5, MemTotal: 1000, MemUsed: 350, MemCached: 150, LimitMax: 800, LimitUsed: 45, ServersCPU: 15, ServersMem: 200, ManagerMem: 25, Running: 2, Servers: 2, Players: 1, CPUMax: 40, Samples: 2},
	}
	if len(pts) != len(want) {
		t.Fatalf("points %+v", pts)
	}
	for i := range want {
		if pts[i] != want[i] {
			t.Errorf("point %d = %+v, want %+v", i, pts[i], want[i])
		}
	}
	if h, err := s.LatestHost(ctx); err != nil || h == nil || h.At != 1050 || h.LimitMax != 800 {
		t.Errorf("latest %+v %v", h, err)
	}
	if err := s.PruneHost(ctx, 1040); err != nil {
		t.Fatal(err)
	}
	if pts, _ := s.HostPoints(ctx, 0, 60); len(pts) != 1 || pts[0].At != 1020 || pts[0].CPU != 5 {
		t.Errorf("after prune %+v", pts)
	}
}

func TestServersPerfPoints(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	for _, p := range []struct {
		id string
		PerfSample
	}{
		{"a", PerfSample{At: 1000, Run: 900, CPU: 10, Mem: 100}},
		{"a", PerfSample{At: 1030, Run: 1020, CPU: 30, Mem: 300}}, // a new run, same bucket
		{"b", PerfSample{At: 1030, Run: 1, CPU: 5, Mem: 50}},
		{"b", PerfSample{At: 500, Run: 1, CPU: 99, Mem: 99}}, // before since
	} {
		if err := s.AddPerfSample(ctx, p.id, p.PerfSample); err != nil {
			t.Fatal(err)
		}
	}
	pts, err := s.ServersPerfPoints(ctx, 900, 60)
	if err != nil {
		t.Fatal(err)
	}
	want := []ServerPerfPoint{
		{Instance: "a", At: 960, CPU: 10, CPUMax: 10, Mem: 100, MemMax: 100, Samples: 1},
		{Instance: "a", At: 1020, CPU: 30, CPUMax: 30, Mem: 300, MemMax: 300, Samples: 1},
		{Instance: "b", At: 1020, CPU: 5, CPUMax: 5, Mem: 50, MemMax: 50, Samples: 1},
	}
	if len(pts) != len(want) {
		t.Fatalf("points %+v", pts)
	}
	for i := range want {
		if pts[i] != want[i] {
			t.Errorf("point %d = %+v, want %+v", i, pts[i], want[i])
		}
	}
}
