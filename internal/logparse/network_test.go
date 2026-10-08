package logparse

import "testing"

func TestParseNetwork(t *testing.T) {
	full := "[network] 3 players, 40 KB/s out, 12 KB/s in, worst ping 80 ms, 2 KB queued (longest 5 ms), 7 failed sends, 1 skipped, 0 dropped" +
		" | loop 120 passes/s, 4% busy, longest pass 3.1 ms, longest gap 9.0 ms (worst since start: 12.5 ms pass, 30.2 ms gap)"
	n, ok := ParseNetwork(full)
	want := Network{Players: 3, OutKBs: 40, InKBs: 12, PingMs: 80, Full: true, QueuedKB: 2, QueueMs: 5, Failed: 7, Skipped: 1,
		Loop: true, BusyPct: 4, PassMs: 3.1, GapMs: 9}
	if !ok || n != want {
		t.Errorf("full line: %+v %v", n, ok)
	}

	n, ok = ParseNetwork("[network] 1 players, 0 KB/s out, 0 KB/s in, worst ping 20 ms")
	if !ok || n != (Network{Players: 1, PingMs: 20}) {
		t.Errorf("head only: %+v %v", n, ok)
	}
	n, ok = ParseNetwork("[network] 2 players, 5 KB/s out, 1 KB/s in, worst ping 30 ms, 0 KB queued (longest 0 ms), 0 failed sends, 0 skipped, 3 dropped")
	if !ok || !n.Full || n.Loop || n.Dropped != 3 {
		t.Errorf("without the loop: %+v %v", n, ok)
	}

	for _, line := range []string{
		// A player named like the line: a join goes on after the name.
		"[network] 9 players, 999 KB/s out, 0 KB/s in, worst ping 0 ms joined (76561198000000001), 1/16",
		"[network] 9 players, 999 KB/s out, 0 KB/s in, worst ping 0 ms left (quit)",
		"[network] 9 players, 999 KB/s out, 0 KB/s in, worst ping 0 ms, x",
		"[network] 3 players, 40 KB/s out, 12 KB/s in, worst ping 80 ms\nsecond line",
		"[network] lots of players",
		"[chat] someone: [network] 1 players, 0 KB/s out, 0 KB/s in, worst ping 20 ms",
	} {
		if n, ok := ParseNetwork(line); ok {
			t.Errorf("%q read as %+v", line, n)
		}
	}

	n, ok = ParseNetwork("[network] 99999999999999999999 players, 1 KB/s out, 1 KB/s in, worst ping 1 ms")
	if !ok || n.Players <= 0 {
		t.Errorf("huge count: %+v %v", n, ok)
	}
}
