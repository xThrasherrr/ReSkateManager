package store

import (
	"context"
	"slices"
	"testing"

	"github.com/xThrasherrr/ReSkateManager/internal/logparse"
)

const (
	tdA = "76561198000000001"
	tdB = "76561198000000002"
	tdC = "76561198000000003"
)

func ranked(entries ...logparse.Result) []logparse.Result {
	for i := range entries {
		entries[i].Place = i + 1
	}
	return entries
}

func res(id, name string, points int64, quit bool) logparse.Result {
	return logparse.Result{Named: logparse.Named{Name: name, ID: id}, Points: points, Quit: quit}
}

// Whoever of those who stayed has the most points wins a Jam or Spot Battle
// two or more played, each of them on a tie; a quit counts for nothing; and
// S.K.A.T.E. with two left, a game alone and one nobody scored in have no
// winner.
func TestThrowdownWins(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	for _, p := range [][2]string{{tdA, "Ann"}, {tdB, "Bob"}, {tdC, "Cat"}} {
		s.SeenPlayer(ctx, "a", p[0], p[1], 100)
	}
	s.SeenPlayer(ctx, "b", tdA, "Ann", 100)
	add := func(instance string, t logparse.Throwdown) {
		if err := s.AddThrowdown(ctx, instance, 1000, t); err != nil {
			panic(err)
		}
	}
	ann := logparse.Named{Name: "Ann", ID: tdA}
	add("a", logparse.Throwdown{Kind: logparse.ThrowdownFinished, Mode: logparse.ModeJam, Leader: ann,
		Results: ranked(res(tdA, "Ann", 12400, false), res(tdB, "Bob", 9100, true), res(tdC, "Cat", 0, false))})
	add("a", logparse.Throwdown{Kind: logparse.ThrowdownFinished, Mode: logparse.ModeSpotBattle, Leader: ann,
		Results: ranked(res(tdA, "Ann", 5000, true), res(tdB, "Bob", 3000, false), res(tdC, "Cat", 3000, false), res("", "Stranger", 10, false))})
	add("a", logparse.Throwdown{Kind: logparse.ThrowdownFinished, Mode: logparse.ModeJam, Leader: ann, Results: ranked(res(tdA, "Ann", 800, false))})
	add("a", logparse.Throwdown{Kind: logparse.ThrowdownFinished, Mode: logparse.ModeJam, Leader: ann,
		Results: ranked(res(tdA, "Ann", 0, false), res(tdB, "Bob", 0, false))})
	add("a", logparse.Throwdown{Kind: logparse.ThrowdownFinished, Mode: logparse.ModeSkate, Leader: ann, Results: []logparse.Result{
		{Named: ann, Landed: 3, Missed: 1}, {Named: logparse.Named{Name: "Bob", ID: tdB}, Landed: 2, Missed: 2}}})
	add("b", logparse.Throwdown{Kind: logparse.ThrowdownFinished, Mode: logparse.ModeSpotBattle, Leader: ann,
		Results: ranked(res(tdA, "Ann", 900, false), res(tdC, "Cat", 100, false))})

	h, err := s.PlayerHistory(ctx, "a", "", 10, "a", "b")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][4]int{tdA: {4, 1, 5, 2}, tdB: {3, 1, 3, 1}, tdC: {2, 1, 3, 1}} // played, won, then on a and b
	for _, p := range h {
		w := want[p.SteamID]
		if p.Throwdowns != w[0] || p.Wins != w[1] || p.ThrowdownsAll == nil || *p.ThrowdownsAll != w[2] || *p.WinsAll != w[3] {
			t.Errorf("%s: %d played, %d won; on both %v, %v; want %v", p.Name, p.Throwdowns, p.Wins, p.ThrowdownsAll, p.WinsAll, w)
		}
	}
	var place, points, landed any
	s.DB.QueryRow(`SELECT place, points, landed FROM throwdown_players p JOIN throwdowns t ON t.id = p.throwdown_id
		WHERE t.mode = 'skate' AND p.steam_id = ?`, tdA).Scan(&place, &points, &landed)
	if place != nil || points != nil || landed != int64(3) {
		t.Errorf("S.K.A.T.E. row: place %v, points %v, landed %v", place, points, landed)
	}
}

// When everyone else quits, the one left wins with points above 0, in every
// mode: for S.K.A.T.E., which has none the server sees, a landed try.
func TestThrowdownForfeits(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	skate := func(id string, landed int, quit bool) logparse.Result {
		return logparse.Result{Named: logparse.Named{Name: id, ID: id}, Landed: landed, Missed: 1, Quit: quit}
	}
	cases := []struct {
		name string
		t    logparse.Throwdown
		won  []bool
	}{
		{"Jam, the leader quit ahead", logparse.Throwdown{Mode: logparse.ModeJam, Results: ranked(res(tdB, "B", 900, true), res(tdA, "A", 300, false))}, []bool{false, true}},
		{"Jam, the one left on 0", logparse.Throwdown{Mode: logparse.ModeJam, Results: ranked(res(tdA, "A", 0, false), res(tdB, "B", 0, true))}, []bool{false, false}},
		{"Spot Battle, two quit", logparse.Throwdown{Mode: logparse.ModeSpotBattle, Results: ranked(res(tdB, "B", 50, true), res(tdA, "A", 10, false), res(tdC, "C", 5, true))}, []bool{false, true, false}},
		{"a mode the server didn't name", logparse.Throwdown{Mode: logparse.ModeOther, Results: ranked(res(tdA, "A", 20, false), res(tdB, "B", 10, false))}, []bool{true, false}},
		{"S.K.A.T.E., the other quit", logparse.Throwdown{Mode: logparse.ModeSkate, Results: []logparse.Result{skate(tdA, 2, false), skate(tdB, 4, true)}}, []bool{true, false}},
		{"S.K.A.T.E., nothing landed", logparse.Throwdown{Mode: logparse.ModeSkate, Results: []logparse.Result{skate(tdA, 0, false), skate(tdB, 4, true)}}, []bool{false, false}},
		{"S.K.A.T.E., two left", logparse.Throwdown{Mode: logparse.ModeSkate, Results: []logparse.Result{skate(tdA, 3, false), skate(tdB, 1, false), skate(tdC, 5, true)}}, []bool{false, false, false}},
		{"everyone quit", logparse.Throwdown{Mode: logparse.ModeJam, Results: ranked(res(tdA, "A", 900, true), res(tdB, "B", 100, true))}, []bool{false, false}},
	}
	for _, c := range cases {
		if err := s.AddThrowdown(ctx, "a", 1000, c.t); err != nil {
			t.Fatal(err)
		}
		var got []bool
		rows, _ := s.DB.Query("SELECT won FROM throwdown_players WHERE throwdown_id = (SELECT MAX(id) FROM throwdowns) ORDER BY seat")
		for rows.Next() {
			var w bool
			rows.Scan(&w)
			got = append(got, w)
		}
		rows.Close()
		if !slices.Equal(got, c.won) {
			t.Errorf("%s: won %v, want %v", c.name, got, c.won)
		}
	}
	// None of the quits counts as played.
	s.SeenPlayer(ctx, "a", tdB, "B", 100)
	if h, _ := s.PlayerHistory(ctx, "a", tdB, 1); len(h) != 1 || h[0].Throwdowns != 2 || h[0].Wins != 0 {
		t.Errorf("B, who quit six of eight: %+v", h)
	}
}

// Forgetting a player takes their places in throwdowns; a throwdown goes
// once nobody in it is left, and removing a server takes all of its own.
func TestThrowdownsPruneAndDelete(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	s.SeenPlayer(ctx, "a", tdA, "Ann", 100) // forgotten below
	s.SeenPlayer(ctx, "a", tdB, "Bob", 5000)
	s.SeenPlayer(ctx, "b", tdB, "Bob", 5000)
	both := logparse.Throwdown{Mode: logparse.ModeJam, Results: ranked(res(tdA, "Ann", 2, false), res(tdB, "Bob", 1, false))}
	annAlone := logparse.Throwdown{Mode: logparse.ModeJam, Results: ranked(res(tdA, "Ann", 2, false), res("", "Stranger", 1, false))}
	s.AddThrowdown(ctx, "a", 200, both)
	s.AddThrowdown(ctx, "a", 300, annAlone)
	s.AddThrowdown(ctx, "b", 300, both)
	count := func(q string) (n int) {
		s.DB.QueryRow(q).Scan(&n)
		return n
	}
	if n, err := s.PrunePlayers(ctx, 1000); err != nil || n != 1 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	if n := count("SELECT COUNT(*) FROM throwdowns WHERE instance_id = 'a'"); n != 1 {
		t.Errorf("%d throwdowns on a, want the one Bob is in", n)
	}
	if n := count("SELECT COUNT(*) FROM throwdown_players WHERE steam_id = '" + tdA + "' AND throwdown_id IN (SELECT id FROM throwdowns WHERE instance_id = 'a')"); n != 0 {
		t.Errorf("Ann keeps %d places on a", n)
	}
	if n := count("SELECT COUNT(*) FROM throwdown_players WHERE name = 'Stranger'"); n != 0 {
		t.Error("the throwdown nobody was left in kept its stranger")
	}
	if n := count("SELECT COUNT(*) FROM throwdowns WHERE instance_id = 'b'"); n != 1 {
		t.Error("b's throwdown went, though Bob is still there")
	}
	if err := s.DeleteInstance(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if n := count("SELECT COUNT(*) FROM throwdown_players p JOIN throwdowns t ON t.id = p.throwdown_id WHERE t.instance_id = 'a'") +
		count("SELECT COUNT(*) FROM throwdown_players WHERE throwdown_id NOT IN (SELECT id FROM throwdowns)"); n != 0 {
		t.Errorf("%d players of a's throwdowns left", n)
	}
}
