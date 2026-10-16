package logparse

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// Each line as Server/server_activity.cpp writes it, unchanged from ReSkate
// 1.0.0 to 2.1.0: names as players pick them, points with thousands commas.
func TestParseThrowdown(t *testing.T) {
	const zee, bob, sam, al, tom, alt, ghost = "76561198000000001", "76561198000000002", "76561198000000003",
		"76561198000000004", "76561198000000005", "76561198000000006", "76561198000000099"
	ids := map[string]string{"Zee": zee, "Bob": bob, "Sam, Jr.": sam, "Al joined Ben": al, "Tom 2": tom, "Zee's Alt": alt}
	who := func(name string) string { return ids[name] }
	n := func(name, id string) Named { return Named{Name: name, ID: id} }
	Z, B := n("Zee", zee), n("Bob", bob)
	cases := []struct {
		line string
		want Throwdown
	}{
		{"Zee placed a Spot Battle drop near (12, -3, 400)", Throwdown{Kind: ThrowdownPlaced, Mode: ModeSpotBattle, Leader: Z, Near: &[3]int{12, -3, 400}}},
		{"Zee placed a S.K.A.T.E. drop", Throwdown{Kind: ThrowdownPlaced, Mode: ModeSkate, Leader: Z}},
		{"Zee placed a throwdown drop", Throwdown{Kind: ThrowdownPlaced, Mode: ModeOther, Leader: Z}},
		{"Zee's Jam drop was removed", Throwdown{Kind: ThrowdownRemoved, Mode: ModeJam, Leader: Z}},
		{"Bob joined Zee's Jam (2 in the queue)", Throwdown{Kind: ThrowdownJoined, Mode: ModeJam, Leader: Z, Player: B, Queue: 2}},
		{"Al joined Ben joined Zee's Jam (3 in the queue)", Throwdown{Kind: ThrowdownJoined, Mode: ModeJam, Leader: Z, Player: n("Al joined Ben", al), Queue: 3}},
		{"Bob joined Zee's Alt's Jam (2 in the queue)", Throwdown{Kind: ThrowdownJoined, Mode: ModeJam, Leader: n("Zee's Alt", alt), Player: B, Queue: 2}},
		{"Stranger joined Zee's Jam (2 in the queue)", Throwdown{Kind: ThrowdownJoined, Mode: ModeJam, Leader: Z, Player: n("Stranger", ""), Queue: 2}},
		{"Bob left the queue for Zee's Spot Battle", Throwdown{Kind: ThrowdownLeft, Mode: ModeSpotBattle, Leader: Z, Player: B}},
		{"Bob quit Zee's S.K.A.T.E.", Throwdown{Kind: ThrowdownQuit, Mode: ModeSkate, Leader: Z, Player: B}},
		{ghost + " quit Zee's Jam", Throwdown{Kind: ThrowdownQuit, Mode: ModeJam, Leader: Z, Player: n(ghost, ghost)}},
		{"Zee's Jam started with 3 players: Zee, Sam, Jr., Bob",
			Throwdown{Kind: ThrowdownStarted, Mode: ModeJam, Leader: Z, Players: []Named{Z, n("Sam, Jr.", sam), B}}},
		{"Zee's Jam started with 1 player: Zee", Throwdown{Kind: ThrowdownStarted, Mode: ModeJam, Leader: Z, Players: []Named{Z}}},
		{"Bob takes the lead in Zee's Jam with 12,400", Throwdown{Kind: ThrowdownLead, Mode: ModeJam, Leader: Z, Player: B, Points: 12400}},
		{"Zee's Spot Battle: Bob scored 3,250 in round 2 (best 1,004,100)",
			Throwdown{Kind: ThrowdownScored, Mode: ModeSpotBattle, Leader: Z, Player: B, Points: 3250, Round: 2, Best: 1004100}},
		{"Zee's Spot Battle: Sam, Jr. scored 0 in round 1", Throwdown{Kind: ThrowdownScored, Mode: ModeSpotBattle, Leader: Z, Player: n("Sam, Jr.", sam), Round: 1}},
		{"Zee's S.K.A.T.E.: Bob landed (turn 4)", Throwdown{Kind: ThrowdownLanded, Mode: ModeSkate, Leader: Z, Player: B, Round: 4}},
		{"Zee's S.K.A.T.E.: Bob missed (turn 5)", Throwdown{Kind: ThrowdownMissed, Mode: ModeSkate, Leader: Z, Player: B, Round: 5}},
		{"Zee's Jam has finished: 1. Tom 2 12,400, 2. Sam, Jr. 9,100 (quit), 3. Zee 0", Throwdown{Kind: ThrowdownFinished, Mode: ModeJam, Leader: Z, Results: []Result{
			{Named: n("Tom 2", tom), Place: 1, Points: 12400},
			{Named: n("Sam, Jr.", sam), Place: 2, Points: 9100, Quit: true},
			{Named: Z, Place: 3},
		}}},
		{"Zee's S.K.A.T.E. has finished: Zee 3 landed / 1 missed, Sam, Jr. 2 landed / 2 missed (quit)", Throwdown{Kind: ThrowdownFinished, Mode: ModeSkate, Leader: Z, Results: []Result{
			{Named: Z, Landed: 3, Missed: 1},
			{Named: n("Sam, Jr.", sam), Landed: 2, Missed: 2, Quit: true},
		}}},
		{"Zee's Spot Battle has finished", Throwdown{Kind: ThrowdownFinished, Mode: ModeSpotBattle, Leader: Z}},
	}
	for _, c := range cases {
		got, ok := ParseThrowdown("[throwdown] "+c.line, who)
		if !ok || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q:\n got %+v\nwant %+v", c.line, got, c.want)
		}
	}
	for _, line := range []string{"[throwdown] Zee did a kickflip", "[objects] Zee placed a Jam drop", "Zee placed a Jam drop",
		"[throwdown] Zee placed a Jam drop\nand more"} {
		if got, ok := ParseThrowdown(line, who); ok {
			t.Errorf("%q read as %+v", line, got)
		}
	}
}

// A name two players share is nobody in particular; a started line whose
// names can't be told apart still keeps the server's count.
func TestParseThrowdownUnknownNames(t *testing.T) {
	none := func(string) string { return "" }
	got, _ := ParseThrowdown("[throwdown] A's Jam started with 2 players: A, B, C", none)
	if len(got.Players) != 2 || got.Players[0].Name != "A" || got.Players[1].Name != "B, C" {
		t.Errorf("players %+v", got.Players)
	}
	got, _ = ParseThrowdown("[throwdown] X joined Y joined Z's Jam (2 in the queue)", none)
	if got.Player.Name != "X" || got.Leader.Name != "Y joined Z" {
		t.Errorf("with neither known, the first cut: %+v", got)
	}
}

// Whatever a line holds, reading it never panics, and a started line keeps
// the server's count of players when it has that many names.
func FuzzParseThrowdown(f *testing.F) {
	for _, s := range []string{"Zee's Jam started with 3 players: Zee, Sam, Jr., Bob", "Zee's Jam has finished: 1. Tom 2 12,400, 2. Sam, Jr. 9,100 (quit)",
		"Zee's S.K.A.T.E. has finished: Zee 3 landed / 1 missed, Bob 0 landed / 2 missed (quit)", "Al joined Ben joined Zee's Jam (3 in the queue)",
		"Zee's Spot Battle: Bob scored 3,250 in round 2 (best 4,100)", "Zee placed a Jam drop near (1, 2, 3)"} {
		f.Add(s, "Zee")
	}
	f.Fuzz(func(t *testing.T, body, name string) {
		got, ok := ParseThrowdown("[throwdown] "+body, func(n string) string {
			if n == name {
				return "76561198000000001"
			}
			return ""
		})
		if ok && got.Kind == ThrowdownStarted && len(got.Players) == 0 {
			t.Errorf("%q started with nobody", body)
		}
	})
}

// 64 players named mostly ", " still read quickly.
func TestParseThrowdownCommaNames(t *testing.T) {
	name := strings.Repeat(", ", 31) + "x"
	names := make([]string, 64)
	for i := range names {
		names[i] = name
	}
	start := time.Now()
	got, ok := ParseThrowdown("[throwdown] Zee's Jam started with 64 players: "+strings.Join(names, ", "), func(n string) string {
		if n == name {
			return "76561198000000001"
		}
		return ""
	})
	if !ok || len(got.Players) != 64 || time.Since(start) > 2*time.Second {
		t.Errorf("%d players in %s", len(got.Players), time.Since(start))
	}
}
