package logparse

import (
	"regexp"
	"strconv"
	"strings"
)

// The throwdowns the server names (Server/server_activity.cpp mode); one it
// doesn't is ModeOther.
const (
	ModeJam        = "jam"
	ModeSpotBattle = "spot_battle"
	ModeSkate      = "skate"
	ModeOther      = ""
)

// What a [throwdown] line says happened.
const (
	ThrowdownPlaced   = "placed"   // the leader put a drop down
	ThrowdownRemoved  = "removed"  // and took it away before it started
	ThrowdownJoined   = "joined"   // a player joined its queue
	ThrowdownLeft     = "left"     // and left the queue before it started
	ThrowdownStarted  = "started"  // it began, with Players
	ThrowdownQuit     = "quit"     // a player quit it once it started
	ThrowdownLead     = "lead"     // Jam: a player took the lead, with Points
	ThrowdownScored   = "scored"   // Spot Battle: a player's round, with Points
	ThrowdownLanded   = "landed"   // S.K.A.T.E.: a player landed their try
	ThrowdownMissed   = "missed"   // or missed it
	ThrowdownFinished = "finished" // it ended, with Results
)

// Throwdown is a [throwdown] line read as what happened. The server logs
// these only with its activity_log on. A line names players as the server
// last knew them, and by SteamID64 when it never knew a name, so a name is
// matched to a player only when one player has it.
type Throwdown struct {
	Kind   string
	Mode   string
	Leader Named // whose throwdown
	Player Named // who the line is about: for joined, left, quit, lead, scored, landed and missed
	Points int64 // lead: their total; scored: what they scored in the round
	Best   int64 // scored: their best round so far, when the server said
	Round  int   // scored: the round; landed and missed: the turn
	Queue  int   // joined: how many are waiting, the leader included
	// Near is where a drop was placed, in metres, when the server said.
	Near    *[3]int
	Players []Named  // started: who plays, in turn order
	Results []Result // finished: by place, or in turn order for S.K.A.T.E.
}

// Named is a player as a line names them, and their SteamID64 when it's
// known who that is.
type Named struct {
	Name string `json:"name"`
	ID   string `json:"id,omitempty"`
}

// Result is one player's line in a finished throwdown.
type Result struct {
	Named
	// Place is 1 for the most points. S.K.A.T.E. has none: it's won on
	// letters, which the server never sees.
	Place          int
	Points         int64
	Landed, Missed int // S.K.A.T.E.'s tries
	Quit           bool
}

var throwdownModes = map[string]string{"Jam": ModeJam, "Spot Battle": ModeSpotBattle, "S.K.A.T.E.": ModeSkate, "throwdown": ModeOther}

const (
	tdMode   = `(Jam|Spot Battle|S\.K\.A\.T\.E\.|throwdown)`
	tdPoints = `(-?\d{1,3}(?:,\d{3})*)`
)

// Each line's fixed words, with the players' names in the groups before
// them. A line naming two players has them in one group, cut by tdCut.
var (
	tdPlacedRe   = regexp.MustCompile(`^(.+) placed a ` + tdMode + ` drop(?: near \((-?\d+), (-?\d+), (-?\d+)\))?$`)
	tdRemovedRe  = regexp.MustCompile(`^(.+)'s ` + tdMode + ` drop was removed$`)
	tdJoinedRe   = regexp.MustCompile(`^(.+ joined .+)'s ` + tdMode + ` \((\d+) in the queue\)$`)
	tdLeftRe     = regexp.MustCompile(`^(.+ left the queue for .+)'s ` + tdMode + `$`)
	tdQuitRe     = regexp.MustCompile(`^(.+ quit .+)'s ` + tdMode + `$`)
	tdStartedRe  = regexp.MustCompile(`^(.+)'s ` + tdMode + ` started with (\d+) players?: (.+)$`)
	tdLeadRe     = regexp.MustCompile(`^(.+ takes the lead in .+)'s ` + tdMode + ` with ` + tdPoints + `$`)
	tdScoredRe   = regexp.MustCompile(`^(.+'s ` + tdMode + `: .+) scored ` + tdPoints + ` in round (\d+)(?: \(best ` + tdPoints + `\))?$`)
	tdTryRe      = regexp.MustCompile(`^(.+'s ` + tdMode + `: .+) (landed|missed) \(turn (\d+)\)$`)
	tdFinishedRe = regexp.MustCompile(`^(.+)'s ` + tdMode + ` has finished(?:: (.+))?$`)
	// A finished line's results: "1. Zee 12,400, 2. Bob 9,100 (quit)", or for
	// S.K.A.T.E. "Zee 3 landed / 1 missed, Bob 2 landed / 2 missed (quit)".
	tdRankedRe = regexp.MustCompile(`^(.+) ` + tdPoints + `( \(quit\))?$`)
	tdTriesRe  = regexp.MustCompile(` (\d+) landed / (\d+) missed( \(quit\))?(?:, |$)`)
	steamIDRe  = regexp.MustCompile(`^7656119\d{10}$`)
)

// ParseThrowdown reads a [throwdown] line. who gives the SteamID64 of the
// one player the server calls a name, or "" for none or more than one; it
// also decides where a line naming two players cuts between them.
func ParseThrowdown(text string, who func(name string) string) (Throwdown, bool) {
	body, ok := strings.CutPrefix(text, "[throwdown] ")
	if !ok || strings.Contains(body, "\n") {
		return Throwdown{}, false
	}
	named := func(name string) Named {
		if steamIDRe.MatchString(name) { // a player whose name the server never knew
			return Named{Name: name, ID: name}
		}
		return Named{Name: name, ID: who(name)}
	}
	if m := tdFinishedRe.FindStringSubmatch(body); m != nil {
		mode := throwdownModes[m[2]]
		return Throwdown{Kind: ThrowdownFinished, Mode: mode, Leader: named(m[1]), Results: results(m[3], mode == ModeSkate, named)}, true
	}
	if m := tdStartedRe.FindStringSubmatch(body); m != nil {
		n, _ := strconv.Atoi(m[3])
		return Throwdown{Kind: ThrowdownStarted, Mode: throwdownModes[m[2]], Leader: named(m[1]), Players: list(m[4], n, named)}, true
	}
	if m := tdScoredRe.FindStringSubmatch(body); m != nil {
		t := Throwdown{Kind: ThrowdownScored, Mode: throwdownModes[m[2]], Points: points(m[3]), Best: points(m[5])}
		t.Round, _ = strconv.Atoi(m[4])
		t.Leader, t.Player = tdCut(m[1], "'s "+m[2]+": ", named)
		return t, true
	}
	if m := tdTryRe.FindStringSubmatch(body); m != nil {
		t := Throwdown{Kind: ThrowdownLanded, Mode: throwdownModes[m[2]]}
		if m[3] == "missed" {
			t.Kind = ThrowdownMissed
		}
		t.Round, _ = strconv.Atoi(m[4])
		t.Leader, t.Player = tdCut(m[1], "'s "+m[2]+": ", named)
		return t, true
	}
	if m := tdLeadRe.FindStringSubmatch(body); m != nil {
		t := Throwdown{Kind: ThrowdownLead, Mode: throwdownModes[m[2]], Points: points(m[3])}
		t.Player, t.Leader = tdCut(m[1], " takes the lead in ", named)
		return t, true
	}
	if m := tdJoinedRe.FindStringSubmatch(body); m != nil {
		t := Throwdown{Kind: ThrowdownJoined, Mode: throwdownModes[m[2]]}
		t.Queue, _ = strconv.Atoi(m[3])
		t.Player, t.Leader = tdCut(m[1], " joined ", named)
		return t, true
	}
	if m := tdLeftRe.FindStringSubmatch(body); m != nil {
		t := Throwdown{Kind: ThrowdownLeft, Mode: throwdownModes[m[2]]}
		t.Player, t.Leader = tdCut(m[1], " left the queue for ", named)
		return t, true
	}
	if m := tdQuitRe.FindStringSubmatch(body); m != nil {
		t := Throwdown{Kind: ThrowdownQuit, Mode: throwdownModes[m[2]]}
		t.Player, t.Leader = tdCut(m[1], " quit ", named)
		return t, true
	}
	if m := tdRemovedRe.FindStringSubmatch(body); m != nil {
		return Throwdown{Kind: ThrowdownRemoved, Mode: throwdownModes[m[2]], Leader: named(m[1])}, true
	}
	if m := tdPlacedRe.FindStringSubmatch(body); m != nil {
		t := Throwdown{Kind: ThrowdownPlaced, Mode: throwdownModes[m[2]], Leader: named(m[1])}
		if m[3] != "" {
			var at [3]int
			for i := range at {
				at[i], _ = strconv.Atoi(m[3+i])
			}
			t.Near = &at
		}
		return t, true
	}
	return Throwdown{}, false
}

// tdCut cuts s into the two names either side of sep. A name may hold sep
// itself, so of the places sep is, it takes the one where the most names are
// players who can be told apart, and the first of those.
func tdCut(s, sep string, named func(string) Named) (a, b Named) {
	best := -1
	for i := strings.Index(s, sep); i >= 0; {
		x, y := named(s[:i]), named(s[i+len(sep):])
		if score := known(x) + known(y); score > best && x.Name != "" && y.Name != "" {
			a, b, best = x, y, score
		}
		next := strings.Index(s[i+1:], sep)
		if next < 0 {
			break
		}
		i += 1 + next
	}
	return a, b
}

func known(n Named) int {
	if n.ID != "" {
		return 1
	}
	return 0
}

// list reads a started line's players, n names joined with ", ". A name may
// hold ", " itself: the reading taken is the one with the most players who
// can be told apart, keeping the server's count.
func list(s string, n int, named func(string) Named) []Named {
	parts := strings.Split(s, ", ")
	if n <= 0 || n >= len(parts) { // no name holds ", "
		out := make([]Named, len(parts))
		for i, p := range parts {
			out[i] = named(p)
		}
		return out
	}
	return group(parts, n, ", ", named)
}

// nameParts bounds how many parts one name is tried as: a name holds ", "
// at most this often, and the reading stays quick under the instance's lock
// whatever names 64 players pick.
const nameParts = 32

// group joins parts, in order, into n names, as list does.
func group(parts []string, n int, sep string, named func(string) Named) []Named {
	// best[i][k]: the most known names among parts[:i] made into k names.
	type cell struct {
		score, from int
		ok          bool
	}
	best := make([][]cell, len(parts)+1)
	for i := range best {
		best[i] = make([]cell, n+1)
	}
	// isKnown[i][d]: whether parts[i-d:i] is a player's name, asked once each.
	isKnown := make([][]int, len(parts)+1)
	for i := 1; i <= len(parts); i++ {
		isKnown[i] = make([]int, min(i, nameParts)+1)
		for d := 1; d < len(isKnown[i]); d++ {
			isKnown[i][d] = known(named(strings.Join(parts[i-d:i], sep)))
		}
	}
	best[0][0].ok = true
	for i := 1; i <= len(parts); i++ {
		// Each name takes one part or more: k names need k parts before, and
		// leave enough after for the rest.
		for k := max(1, n-(len(parts)-i)); k <= min(n, i); k++ {
			for j := max(k-1, i-nameParts); j < i; j++ {
				if !best[j][k-1].ok {
					continue
				}
				score := best[j][k-1].score + isKnown[i][i-j]
				if c := &best[i][k]; !c.ok || score > c.score {
					*c = cell{score, j, true}
				}
			}
		}
	}
	if !best[len(parts)][n].ok { // more commas than names can hold: the last takes the rest
		out := make([]Named, n)
		for k := range n - 1 {
			out[k] = named(parts[k])
		}
		out[n-1] = named(strings.Join(parts[n-1:], sep))
		return out
	}
	out := make([]Named, n)
	for i, k := len(parts), n; k > 0; k-- {
		j := best[i][k].from
		out[k-1] = named(strings.Join(parts[j:i], sep))
		i = j
	}
	return out
}

// results reads a finished line's results.
func results(s string, skate bool, named func(string) Named) []Result {
	if s == "" {
		return nil
	}
	var out []Result
	if skate {
		at := 0
		for _, m := range tdTriesRe.FindAllStringSubmatchIndex(s, -1) {
			r := Result{Named: named(s[at:m[0]]), Quit: m[6] >= 0}
			r.Landed, _ = strconv.Atoi(s[m[2]:m[3]])
			r.Missed, _ = strconv.Atoi(s[m[4]:m[5]])
			out, at = append(out, r), m[1]
		}
		return out
	}
	// Each entry opens with its place, "1. ", then ", 2. " and so on: the
	// points' own commas are never followed by a space.
	for place, rest := 1, strings.TrimPrefix(s, "1. "); rest != ""; place++ {
		entry := rest
		if i := strings.Index(rest, ", "+strconv.Itoa(place+1)+". "); i >= 0 {
			entry, rest = rest[:i], rest[i+len(", "+strconv.Itoa(place+1)+". "):]
		} else {
			rest = ""
		}
		m := tdRankedRe.FindStringSubmatch(entry)
		if m == nil {
			continue
		}
		out = append(out, Result{Named: named(m[1]), Place: place, Points: points(m[2]), Quit: m[3] != ""})
	}
	return out
}

// points reads "12,400"; 0 for "".
func points(s string) int64 {
	n, _ := strconv.ParseInt(strings.ReplaceAll(s, ",", ""), 10, 64)
	return n
}
