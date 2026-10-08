package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/xThrasherrr/ReSkateManager/internal/logparse"
)

func TestNetPoints(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	full := func(players, out int, failed int64) logparse.Network {
		return logparse.Network{Players: players, OutKBs: out, InKBs: 1, PingMs: 50 + players, Full: true, QueuedKB: players, Failed: failed,
			Loop: true, BusyPct: 10 * players, PassMs: 2.5}
	}
	for _, r := range []struct {
		at, run int64
		n       logparse.Network
	}{
		{1000, 900, full(1, 10, 4)},
		{1060, 900, full(3, 30, 9)},  // 5 more failed sends this minute
		{1130, 1100, full(2, 20, 1)}, // a new run counts from its own first line
		{1190, 1100, full(2, 20, 3)},
		{1250, 1100, logparse.Network{Players: 1, OutKBs: 5, PingMs: 9}}, // an older server's line
	} {
		if err := s.AddNetSample(ctx, "a", r.at, r.run, r.n); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AddNetSample(ctx, "b", 1000, 1, full(9, 99, 99)); err != nil {
		t.Fatal(err)
	}
	pts, err := s.NetPoints(ctx, "a", 0, 120)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 3 {
		t.Fatalf("points %+v", pts)
	}
	// 960-1079: both of run 900's lines; only the second has a minute before it.
	if p := pts[0]; p.At != 960 || p.Players != 3 || p.Out != 20 || p.OutMax != 30 || p.Ping != 53 || *p.Failed != 5 || *p.Busy != 30 || *p.Queued != 3 {
		t.Errorf("first bucket %+v", p)
	}
	// 1080-1199: run 1100's first two lines.
	if p := pts[1]; p.At != 1080 || *p.Failed != 2 {
		t.Errorf("second bucket %+v failed %v", p, p.Failed)
	}
	// 1200-: the older server's line has none of the rest.
	if p := pts[2]; p.At != 1200 || p.Failed != nil || p.Busy != nil || p.Queued != nil || p.Players != 1 {
		t.Errorf("third bucket %+v", p)
	}

	if err := s.PruneNet(ctx, 1100); err != nil {
		t.Fatal(err)
	}
	if pts, _ := s.NetPoints(ctx, "a", 0, 120); len(pts) != 2 {
		t.Errorf("after pruning: %+v", pts)
	}
}
