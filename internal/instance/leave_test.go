package instance

import (
	"context"
	"log/slog"
	"slices"
	"testing"
	"time"
)

// Each visit OnJoin opens gets one leave, however the player goes from the
// roster: their leave line, one naming two players, a roster check, a join
// again, the server exiting on its own, and the manager stopping it.
func TestLeaves(t *testing.T) {
	reg := NewRegistry(slog.New(slog.DiscardHandler))
	joined := make(chan Player, 16)
	left := make(chan Leave, 16)
	slow := make(chan struct{}) // closed: OnLeave takes a while, as a database write can
	reg.Wire = func(in *Instance) {
		in.OnJoin = func(p Player) { joined <- p }
		in.OnLeave = func(l Leave) {
			select {
			case <-slow:
				time.Sleep(300 * time.Millisecond)
			default:
			}
			left <- l
		}
	}
	in := reg.Add(fakeServer(t))
	t.Cleanup(func() { reg.StopAll(10 * time.Second) })
	if err := in.Start(); err != nil {
		t.Fatal(err)
	}
	waitState(t, in, Running)
	run := func(cmds ...string) {
		t.Helper()
		for _, c := range cmds {
			in.Command(context.Background(), c, "test") // crash has no reply
		}
	}
	visits := map[string]int64{} // the visit each player's join opened, by SteamID
	takeJoins := func(n int) {
		t.Helper()
		for range n {
			select {
			case p := <-joined:
				visits[p.ID] = p.JoinedAt
			case <-time.After(5 * time.Second):
				t.Fatal("no join")
			}
		}
	}
	takeLeaves := func(want map[string]string) { // reasons by SteamID
		t.Helper()
		for len(want) > 0 {
			select {
			case l := <-left:
				if reason, ok := want[l.ID]; !ok || l.Reason != reason || l.JoinedAt != visits[l.ID] || l.At < l.JoinedAt {
					t.Fatalf("leave %+v, want one of %v for the visit from %d", l, want, visits[l.ID])
				}
				delete(want, l.ID)
			case <-time.After(5 * time.Second):
				t.Fatalf("no leave for %v", want)
			}
		}
	}
	roster := func() []string {
		var ids []string
		for _, p := range in.Players() {
			ids = append(ids, p.ID)
		}
		slices.Sort(ids)
		return ids
	}
	const alice, bob, sam1, sam2, cat = "76561198000000001", "76561198000000002", "76561198000000003", "76561198000000004", "76561198000000005"

	run("join "+alice+" Alice", "join "+bob+" Bob")
	takeJoins(2)
	run("leave " + alice)
	takeLeaves(map[string]string{alice: "Disconnected"})

	// Two of one name: the roster check finds out which, and the leave keeps its reason.
	run("join "+sam1+" Sam", "join "+sam2+" Sam")
	takeJoins(2)
	run("leave " + sam1)
	takeLeaves(map[string]string{sam1: "Disconnected"})
	if got := roster(); !slices.Equal(got, []string{bob, sam2}) {
		t.Fatalf("roster %v after one Sam left", got)
	}

	// Gone with no line: the next count that disagrees finds them.
	run("vanish "+bob, "join "+cat+" Cat")
	takeJoins(1)
	takeLeaves(map[string]string{bob: LeftNotOn})

	// Joined again with no leave between: the visit before ends there.
	before := visits[cat]
	run("join " + cat + " Cat")
	takeLeaves(map[string]string{cat: LeftNotOn})
	takeJoins(1)
	if visits[cat] == before {
		t.Fatal("the join again opened no new visit")
	}

	run("crash")
	takeLeaves(map[string]string{sam2: LeftExited, cat: LeftExited})
	waitState(t, in, Crashed)

	// Stopping the manager waits for the leaves the stop sent.
	if err := in.Start(); err != nil {
		t.Fatal(err)
	}
	waitState(t, in, Running)
	run("join " + alice + " Alice")
	takeJoins(1)
	close(slow)
	reg.StopAll(10 * time.Second)
	select {
	case l := <-left:
		if l.ID != alice || l.Reason != LeftStopped || l.JoinedAt != visits[alice] {
			t.Errorf("leave on stop %+v", l)
		}
	default:
		t.Fatal("StopAll returned before the leave was handled")
	}
	select {
	case l := <-left:
		t.Errorf("a leave too many: %+v", l)
	case p := <-joined:
		t.Errorf("a join too many: %+v", p)
	case <-time.After(200 * time.Millisecond):
	}
}
