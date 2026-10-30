package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/store"
)

// readTime sets readWait for one test, as callTime does CallTime.
func readTime(t *testing.T, d time.Duration) {
	old := readWait
	t.Cleanup(func() { readWait = old })
	readWait = d
}

// checkLines checks a plugin's log: line i starts "i " and holds want[i-1].
func checkLines(t *testing.T, m *Manager, id string, want []string) {
	t.Helper()
	got := lines(m, id)
	if len(got) != len(want) {
		t.Fatalf("%s: %d lines, want %d:\n%s", id, len(got), len(want), strings.Join(got, "\n"))
	}
	for i, w := range want {
		if !strings.HasPrefix(got[i], fmt.Sprint(i+1)+" ") || !strings.Contains(got[i], w) {
			t.Errorf("%s line %d: %q, want %q", id, i+1, got[i], w)
		}
	}
}

// The store keeps a plugin's values as they were, through a fresh start
// and a restart of the manager, within its limits.
func TestStore(t *testing.T) {
	callTime(t, 5*time.Second)
	m := newManager(t)
	addPlugin(t, m, "keeper", "", `
store.set("n", 42) store.set("f", 1.5) store.set("s", "hi") store.set("b", false)
store.set("t", {1, 2, x = {y = "z"}})
local t = store.get("t")
log(1, store.get("n"), math.type(store.get("n")), store.get("f"), store.get("s"), store.get("b"), t[1], t[2], t.x.y)
log(2, store.get("missing"))
log(3, table.concat(store.keys(), ","), table.concat(store.keys("f"), ","), #store.keys("zz"))
log(4, store.delete("s"), store.delete("s"), store.get("s"))
store.set("n", nil)
log(5, store.get("n"))
for i, f in ipairs({
  function() store.set("fn", function() end) end,
  function() store.set("deep", {f = print}) end,
  function() store.set("", 1) end,
  function() store.set(("k"):rep(257), 1) end,
  function() store.set("big", ("x"):rep(70000)) end,
  function() store.set(7, 1) end,
}) do
  log(5 + i, select(2, pcall(f)))
end
local n = 0
while true do
  local ok, err = pcall(store.set, "fill" .. n, ("x"):rep(60000))
  if not ok then log(12, n, err) break end
  n = n + 1
end
for i = 0, n - 1 do store.delete("fill" .. i) end
`)
	enable(t, m, "keeper")
	want := []string{
		"1 42 integer 1.5 hi false 1 2 z",
		"2 nil",
		"3 b,f,n,s,t f 0",
		"4 true false nil",
		"5 nil",
		"bad argument #2 to 'store.set' (a function can't be kept)",
		"bad argument #2 to 'store.set' (a function can't be kept)",
		"bad argument #1 to 'store.set' (a key of 1 to 256 bytes)",
		"bad argument #1 to 'store.set' (a key of 1 to 256 bytes)",
		"bad argument #2 to 'store.set' (a value takes 64 KB at most)",
		"bad argument #1 to 'store.set' (a key of 1 to 256 bytes)",
		"12 17 store.set: the plugin's store holds 1 MB at most",
	}
	checkLines(t, m, "keeper", want)
	// A fresh Lua finds them, and so does the next manager.
	addPlugin(t, m, "keeper", "", `log(store.get("t").x.y, store.get("f"), #store.keys())`)
	if err := m.Reload("keeper"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return hasLine(m, "keeper", "z 1.5 3") })
	m.Close()
	again := &Manager{Dir: m.Dir, Store: m.Store, Log: m.Log, LogPath: m.LogPath}
	if err := again.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	waitFor(t, func() bool { return hasLine(again, "keeper", "z 1.5 3") })
}

// Boards: a plugin's own to add to, and every plugin's to read, network-wide
// or a server's; playtime among them.
func TestBoards(t *testing.T) {
	callTime(t, 5*time.Second) // reads after adds wait for the board, which -race slows
	readTime(t, 4*time.Second)
	m := newManager(t)
	servers(t, m, "s1", "s2")
	const p1, p2, p3 = "76561198000000001", "76561198000000002", "76561198000000003"
	ctx := context.Background()
	now := time.Now().Unix()
	if err := m.Store.SeenPlayer(ctx, "s1", p1, "Zee", now-3600); err != nil {
		t.Fatal(err)
	}
	if err := m.Store.LeftPlayer(ctx, "s1", p1, now-3600, now-600, "Disconnected."); err != nil {
		t.Fatal(err)
	}
	if err := m.Store.SeenPlayer(ctx, "s2", p2, "Bea", now-60); err != nil {
		t.Fatal(err)
	}
	show := `
local P1, P2, P3 = "` + p1 + `", "` + p2 + `", "` + p3 + `"
local function show(list)
  local out = {}
  for _, r in ipairs(list) do out[#out + 1] = r.rank .. ":" .. r.id:sub(-1) .. ":" .. r.name .. "=" .. tostring(r.value) end
  return table.concat(out, " ")
end
local function try(i, f, ...) log(i, select(2, pcall(f, ...))) end
`
	addPlugin(t, m, "a", "", show+`
stats.define("wins", {title = "Wins", unit = "wins"})
stats.define("lap", {title = "Fastest lap", unit = "s", keep = "fastest", public = true})
stats.add("wins", "s1", P1, 1)
stats.add("wins", "s2", P1, 2)
stats.add("wins", "s1", P2, 3)
stats.add("lap", "s1", P1, 62.5)
stats.add("lap", "s1", P1, 61.25)
stats.add("lap", "s2", P2, 61.25)
log(1, show(stats.top("wins")))
log(2, show(stats.top("wins", {server = "s1"})))
log(3, stats.rank("wins", P1, {server = "s2"}))
log(4, stats.rank("wins", P3))
log(5, show(stats.top("lap")))
log(6, show(stats.around("wins", P2, {limit = 1})), #stats.around("wins", P3))
try(7, stats.add, "wins", "s3", P1, 1)
try(8, stats.add, "nope", "s1", P1, 1)
try(9, stats.add, "playtime", "s1", P1, 1)
try(10, stats.define, "lap", {keep = "sum"})
try(11, stats.define, "Bad ID")
try(12, stats.define, "x", {kep = "sum"})
try(13, stats.top, "wins", {period = "year"})
try(14, stats.top, "wins", {limit = 101})
try(15, stats.add, "wins", "s1", P1, 0/0)
try(16, stats.rank, "wins", P1, {limit = 3})
try(17, stats.top, "wins", {server = "s9"})
log(18, show(stats.top("wins", {period = "week"})), show(stats.top("wins", {period = "month", limit = 1})))
log(19, playtime.total(P1), playtime.total(P1, "s2"), playtime.total(P3))
local v = playtime.sessions(P1)
log(20, #v, v[1].server, v[1].left_at - v[1].joined_at, v[1].reason, playtime.sessions(P2)[1].left_at)
log(21, show(stats.top("playtime", {limit = 1})), stats.rank("playtime", P1, {server = "s2"}))
`)
	enable(t, m, "a")
	addPlugin(t, m, "b", "", show+`
try(1, stats.define, "wins")
try(2, stats.add, "wins", "s1", P1, 1)
log(3, show(stats.top("wins")))
`)
	enable(t, m, "b")
	checkLines(t, m, "a", []string{
		"1 1:1:Zee=3 1:2:Bea=3", // equal, and in the same second: by SteamID
		"2 1:2:Bea=3 2:1:Zee=1",
		"3 1 2",
		"4 nil",
		"5 1:1:Zee=61.25 1:2:Bea=61.25",
		"6 1:2:Bea=3 0",
		`bad argument #2 to 'stats.add' (no server "s3")`,
		`bad argument #1 to 'stats.add' (no board "nope"): define it with stats.define first`,
		`bad argument #1 to 'stats.add' (playtime adds itself up)`,
		`board "lap" is kept as fastest; that can't change, so give it a new ID`,
		`bad argument #1 to 'stats.define' (a board ID`,
		`bad argument #2 to 'stats.define' (no option kep; it takes title, unit, keep, public)`,
		`bad argument #2 to 'stats.top' (period is "all", "month" or "week")`,
		`bad argument #2 to 'stats.top' (a limit from 1 to 100)`,
		`bad argument #4 to 'stats.add' (a number, not NaN or infinity)`,
		`bad argument #3 to 'stats.rank' (no option limit; it takes server, period, at)`,
		`bad argument #2 to 'stats.top' (no server s9)`,
		"18 1:1:Zee=3 1:2:Bea=3 1:1:Zee=3",
		"19 3000 0 0",
		"20 1 s1 3000 Disconnected. nil",
		"21 1:1:Zee=3000 nil",
	})
	checkLines(t, m, "b", []string{
		`board "wins" is plugin "a"'s`,
		`bad argument #1 to 'stats.add' (board "wins" is plugin "a"'s)`,
		"3 1:1:Zee=3 1:2:Bea=3",
	})
	// The panel reads playtime and the public boards, network-wide, without SteamIDs.
	got, _ := json.Marshal(m.PublicBoards("", "all", 1))
	if want := `[{"id":"playtime","title":"Playtime","unit":"seconds","keep":"sum","rows":[{"rank":1,"name":"Zee","value":3000}]},` +
		`{"id":"lap","title":"Fastest lap","unit":"s","keep":"fastest","rows":[{"rank":1,"name":"Zee","value":61.25}]}]`; string(got) != want {
		t.Errorf("public boards:\n%s\nwant\n%s", got, want)
	}
	// Or one server's: on s2, Bea's the only one.
	if s2 := m.PublicBoards("s2", "all", 1); len(s2) != 2 || len(s2[0].Rows) != 1 || s2[0].Rows[0].Name != "Bea" ||
		fmt.Sprint(s2[1].Rows) != "[{1 Bea 61.25}]" {
		t.Errorf("s2's public boards: %+v", s2)
	}
	// What they defined outlives the manager.
	m.Close()
	list, err := m.Store.Boards(ctx)
	if err != nil || fmt.Sprint(list) != "[{lap a Fastest lap s fastest true} {wins a Wins wins sum false}]" {
		t.Errorf("boards: %v, %v", list, err)
	}
}

// onClock puts the manager's clock in zone for one test.
func onClock(t *testing.T, zone string) *time.Location {
	loc, err := time.LoadLocation(zone)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Local
	t.Cleanup(func() { time.Local = old })
	time.Local = loc
	return loc
}

// stats.period: when the month or week holding a time starts and ends on the
// manager's clock, and its name, a DST change included.
func TestPeriod(t *testing.T) {
	loc := onClock(t, "America/Denver")
	at := func(s string) int64 {
		tm, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
		if err != nil {
			t.Fatal(err)
		}
		return tm.Unix()
	}
	m := newManager(t)
	addPlugin(t, m, "a", "", fmt.Sprintf(`
local function try(i, f, ...) log(i, select(2, pcall(f, ...))) end
log(1, stats.period("month", %d))
log(2, stats.period("month", %d))
log(3, stats.period("month", %d))
log(4, stats.period("week", %d))
log(5, stats.period("month"))
try(6, stats.period, "all")
try(7, stats.period, "year", %d)
try(8, stats.period, "month", -1)
try(9, stats.period, "month", 1.5)
try(10, stats.period, "month", "x")
`, at("2026-10-31 23:59"), at("2026-11-01 00:00"), at("2026-12-10 12:00"), at("2026-10-28 12:00"), at("2026-10-28 12:00")))
	enable(t, m, "a")
	now := time.Now()
	month := store.PeriodStart("month", now)
	next := store.PeriodStart("month", time.Unix(month, 0).AddDate(0, 1, 0))
	checkLines(t, m, "a", []string{
		fmt.Sprintf("1 %d %d 2026-10", at("2026-10-01 00:00"), at("2026-11-01 00:00")),
		fmt.Sprintf("2 %d %d 2026-11", at("2026-11-01 00:00"), at("2026-12-01 00:00")), // MDT to MST: 30 days and an hour
		fmt.Sprintf("3 %d %d 2026-12", at("2026-12-01 00:00"), at("2027-01-01 00:00")),
		fmt.Sprintf("4 %d %d 2026-10-26", at("2026-10-26 00:00"), at("2026-11-02 00:00")), // a Wednesday's week, over the change
		fmt.Sprintf("5 %d %d %s", month, next, time.Unix(month, 0).Format("2006-01")),
		`bad argument #1 to 'stats.period' (period is "month" or "week")`,
		`bad argument #1 to 'stats.period' (period is "month" or "week")`,
		`bad argument #2 to 'stats.period' (a time in whole unix seconds)`,
		`bad argument #2 to 'stats.period' (a time in whole unix seconds)`,
		`bad argument #2 to 'stats.period' (a time in whole unix seconds)`,
	})
}

// at reads a board over a past month or week, not over now's, and refuses a
// time to come and playtime.
func TestBoardsAt(t *testing.T) {
	callTime(t, 5*time.Second)
	readTime(t, 4*time.Second)
	m := newManager(t)
	servers(t, m, "s1")
	const p1, p2 = "76561198000000001", "76561198000000002"
	last := store.PeriodStart("month", time.Now()) - 86400 // a day before this month began
	ctx := context.Background()
	if err := m.Store.SaveBoards(ctx, []store.Board{{ID: "wins", Plugin: "a", Title: "wins", Keep: store.KeepSum}}); err != nil {
		t.Fatal(err)
	}
	if err := m.Store.AddScores(ctx, []store.Score{
		{Board: "wins", Keep: store.KeepSum, Server: "s1", SteamID: p1, Value: 5, At: last},
		{Board: "wins", Keep: store.KeepSum, Server: "s1", SteamID: p2, Value: 2, At: last},
	}); err != nil {
		t.Fatal(err)
	}
	addPlugin(t, m, "a", "", `
local P1, P2 = "`+p1+`", "`+p2+`"
local function show(list)
  local out = {}
  for _, r in ipairs(list) do out[#out + 1] = r.rank .. ":" .. r.id:sub(-1) .. "=" .. tostring(r.value) end
  return table.concat(out, " ")
end
local function try(i, f, ...) log(i, select(2, pcall(f, ...))) end
stats.define("wins")
stats.add("wins", "s1", P2, 3)
local month, ends = stats.period("month")
log(1, show(stats.top("wins", {period = "month", at = month - 1})))
log(2, show(stats.top("wins", {period = "month"})))
local rank, value = stats.rank("wins", P1, {period = "month", at = month - 1})
log(3, rank, value, stats.rank("wins", P1, {period = "month"}))
log(4, show(stats.around("wins", P2, {period = "month", at = month - 1, limit = 1})))
log(5, show(stats.top("wins", {at = month - 1})))
try(6, stats.top, "wins", {period = "month", at = ends})
try(7, stats.top, "playtime", {period = "month", at = month - 1})
try(8, stats.top, "wins", {at = 1.5})
try(9, stats.rank, "wins", P1, {limit = 3})
`)
	enable(t, m, "a")
	checkLines(t, m, "a", []string{
		"1 1:1=5 2:2=2",
		"2 1:2=3",
		"3 1 5 nil",
		"4 2:2=2",
		"5 1:1=5 1:2=5", // all time is all time, whatever at says
		`bad argument #2 to 'stats.top' (at can't be later than now)`,
		`bad argument #2 to 'stats.top' (playtime is read as it is now, without at)`,
		`bad argument #2 to 'stats.top' (a time in whole unix seconds)`,
		`bad argument #3 to 'stats.rank' (no option limit; it takes server, period, at)`,
	})
}

// A score read straight after it's added counts, and one added since a
// board was last read is in its next read.
func TestBoardsReadTheirWrites(t *testing.T) {
	callTime(t, 5*time.Second)
	readTime(t, 4*time.Second)
	m := newManager(t)
	servers(t, m, "s1")
	addPlugin(t, m, "race", "", `
stats.define("lap", {keep = "fastest"})
local P = "`+zee+`"
for i, t in ipairs({70, 65, 68, 60}) do
  stats.add("lap", "s1", P, t)
  log(i, stats.rank("lap", P))
end
`)
	enable(t, m, "race")
	if got := strings.Join(lines(m, "race"), ", "); got != "1 1 70, 2 1 65, 3 1 65, 4 1 60" {
		t.Errorf("got %s", got)
	}
}

// A plugin can't have more than maxWaiting scores waiting, though another
// for the same rows merges with its own.
func TestScoresWaiting(t *testing.T) {
	m := newManager(t)
	d := newData(m.Store, m.Log)
	d.boards["b"] = store.Board{ID: "b", Plugin: "p", Keep: store.KeepBest}
	at := time.Now().Unix()
	for i := range maxWaiting {
		if !d.add("p", store.Score{Board: "b", Keep: store.KeepBest, Server: "s1", SteamID: fmt.Sprint(i), Value: 1, At: at}) {
			t.Fatalf("refused score %d", i)
		}
	}
	if d.add("p", store.Score{Board: "b", Keep: store.KeepBest, Server: "s1", SteamID: "one more", Value: 1, At: at}) {
		t.Error("took one past maxWaiting")
	}
	if !d.add("p", store.Score{Board: "b", Keep: store.KeepBest, Server: "s1", SteamID: "0", Value: 5, At: at + 1}) {
		t.Error("refused one that merges")
	}
	if s := d.scores[scoreKey{"b", "s1", "0", store.PeriodStart("month", time.Unix(at, 0)), store.PeriodStart("week", time.Unix(at, 0))}]; s == nil || s.Value != 5 || s.At != at+1 {
		t.Errorf("merged: %+v", s)
	}
}

// merge keeps what each kind of board keeps, whichever score is older; an
// equal best goes to whoever had it first.
func TestMerge(t *testing.T) {
	for _, c := range []struct {
		keep       string
		a, b, want store.Score
	}{
		{store.KeepSum, store.Score{Value: 2, At: 5}, store.Score{Value: 3, At: 4}, store.Score{Value: 5, At: 5}},
		{store.KeepBest, store.Score{Value: 2, At: 5}, store.Score{Value: 3, At: 4}, store.Score{Value: 3, At: 4}},
		{store.KeepBest, store.Score{Value: 3, At: 5}, store.Score{Value: 3, At: 4}, store.Score{Value: 3, At: 4}},
		{store.KeepBest, store.Score{Value: 3, At: 5}, store.Score{Value: 2, At: 6}, store.Score{Value: 3, At: 5}},
		{store.KeepFastest, store.Score{Value: 2, At: 5}, store.Score{Value: 3, At: 4}, store.Score{Value: 2, At: 5}},
		{store.KeepFastest, store.Score{Value: 3, At: 5}, store.Score{Value: 2, At: 6}, store.Score{Value: 2, At: 6}},
	} {
		s := c.a
		s.Keep, c.b.Keep, c.want.Keep = c.keep, c.keep, c.keep
		merge(&s, c.b)
		if s != c.want {
			t.Errorf("%s: %+v into %+v: %+v, want %+v", c.keep, c.b, c.a, s, c.want)
		}
	}
}

// A burst of scores is written in batches, between which the audit log's
// writes get their turns.
func TestScoresBurst(t *testing.T) {
	callTime(t, 5*time.Second)
	m := newManager(t)
	servers(t, m, "s1")
	const n = 9000
	addPlugin(t, m, "burst", "", fmt.Sprintf(`
stats.define("points")
for i = 1, %d do
  assert(stats.add("points", "s1", string.format("765611980%%08d", i), i))
end
log("queued")
`, n))
	enable(t, m, "burst")
	if !hasLine(m, "burst", "queued") {
		t.Fatalf("%q", lines(m, "burst"))
	}
	var slowest time.Duration
	var first time.Time
	written := func() int {
		var c int
		m.Store.DB.QueryRow("SELECT COUNT(*) FROM board_scores").Scan(&c)
		if c > 0 && first.IsZero() {
			first = time.Now()
		}
		return c
	}
	for deadline := time.Now().Add(60 * time.Second); written() < 3*n && time.Now().Before(deadline); {
		start := time.Now()
		if err := m.Store.Audit(context.Background(), store.AuditEntry{Action: "test", Detail: "while scores are written"}); err != nil {
			t.Fatal(err)
		}
		if !first.IsZero() {
			slowest = max(slowest, time.Since(start))
		}
		time.Sleep(2 * time.Millisecond)
	}
	if c := written(); c != 3*n {
		t.Fatalf("%d rows written, want %d", c, 3*n)
	}
	// Machines differ in speed, so it's measured against the whole write: an
	// audit write waits for a batch, not the burst.
	took := time.Since(first)
	t.Logf("slowest audit write while %d scores were written, in %s: %s", n, took, slowest)
	if slowest > took/4 {
		t.Errorf("an audit write waited %s of the %s", slowest, took)
	}
}

// A snapshot that's only old is read at once and worked out again behind
// it; past maxSnaps, the one read longest ago goes.
func TestSnapshots(t *testing.T) {
	readTime(t, 4*time.Second)
	m := newManager(t)
	d := m.data
	b, _ := d.board(store.PlaytimeBoard)
	first, err := d.ranking(b, "", "all", time.Now(), readWait)
	if err != nil {
		t.Fatal(err)
	}
	k := snapKey{b.ID, "", "all", 0}
	d.mu.Lock()
	d.snaps[k].at = time.Now().Add(-snapAge)
	d.mu.Unlock()
	if again, err := d.ranking(b, "", "all", time.Now(), readWait); err != nil || again != first {
		t.Fatalf("an old snapshot: %p, %v, want the one there, %p", again, err, first)
	}
	waitFor(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.snaps[k].cur != first && d.snaps[k].ready == nil
	})
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range maxSnaps - 1 {
		d.snaps[snapKey{board: fmt.Sprint(i)}] = &snapshot{used: time.Now().Add(time.Duration(i) * time.Second)}
	}
	d.snaps[k].used = time.Now().Add(-time.Hour)
	d.evictLocked()
	if _, ok := d.snaps[k]; ok || len(d.snaps) != maxSnaps-1 {
		t.Errorf("evicted the wrong one: %d left", len(d.snaps))
	}
}
