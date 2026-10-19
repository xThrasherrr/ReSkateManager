package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func defineBoard(t *testing.T, s *Store, id, keep string) Board {
	t.Helper()
	b := Board{ID: id, Plugin: "p", Title: id, Keep: keep}
	if err := s.SaveBoards(context.Background(), []Board{b}); err != nil {
		t.Fatal(err)
	}
	return b
}

// rows is a board's players as "rank name-or-id value" lines.
func rows(list []Ranked, err error) string {
	if err != nil {
		return "error: " + err.Error()
	}
	var out []string
	for _, r := range list {
		who := r.Name
		if who == "" {
			who = r.SteamID
		}
		out = append(out, fmt.Sprintf("%d %s %v", r.Rank, who, r.Value))
	}
	return strings.Join(out, ", ")
}

// Each way of keeping a score, on two servers apart and together, with ties
// going to whoever got there first.
func TestBoardKeeps(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	// Names are each player's latest; D and C were never seen.
	for _, v := range []struct {
		server, id, name string
		at               int64
	}{{"s1", "A", "Alice", 100}, {"s2", "A", "Ali", 200}, {"s1", "B", "Bob", 100}} {
		if err := s.SeenPlayer(ctx, v.server, v.id, v.name, v.at); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC).Unix()
	adds := []struct {
		server, id string
		v          float64
	}{{"s1", "A", 5}, {"s1", "A", 3}, {"s2", "A", 4}, {"s1", "B", 12}, {"s2", "C", 2.5}, {"s2", "D", 12}}
	want := map[string]map[string]string{
		KeepSum: {
			"":   "1 Ali 12, 1 Bob 12, 1 D 12, 4 C 2.5",
			"s1": "1 Bob 12, 2 Ali 8",
			"s2": "1 D 12, 2 Ali 4, 3 C 2.5",
		},
		KeepBest: {
			"":   "1 Bob 12, 1 D 12, 3 Ali 5, 4 C 2.5",
			"s1": "1 Bob 12, 2 Ali 5",
			"s2": "1 D 12, 2 Ali 4, 3 C 2.5",
		},
		KeepFastest: {
			"":   "1 C 2.5, 2 Ali 3, 3 Bob 12, 3 D 12",
			"s1": "1 Ali 3, 2 Bob 12",
			"s2": "1 C 2.5, 2 Ali 4, 3 D 12",
		},
	}
	for _, keep := range []string{KeepSum, KeepBest, KeepFastest} {
		b := defineBoard(t, s, "b-"+keep, keep)
		var list []Score
		for i, a := range adds {
			list = append(list, Score{Board: b.ID, Keep: keep, Server: a.server, SteamID: a.id, Value: a.v, At: at + int64(i)})
		}
		if err := s.AddScores(ctx, list[:3]); err != nil { // a batch at a time, as they're flushed
			t.Fatal(err)
		}
		if err := s.AddScores(ctx, list[3:]); err != nil {
			t.Fatal(err)
		}
		for _, server := range []string{"", "s1", "s2"} {
			q := BoardQuery{Board: b, Server: server, Period: "all", Now: time.Unix(at, 0)}
			if got := rows(s.Ranking(ctx, q)); got != want[keep][server] {
				t.Errorf("%s on %q: %s, want %s", keep, server, got, want[keep][server])
			}
		}
		if got := rows(s.Ranking(ctx, BoardQuery{Board: b, Period: "week", Now: time.Unix(at, 0)})); got != want[keep][""] {
			t.Errorf("%s this week: %s", keep, got)
		}
	}
}

// onClock sets the manager's clock for one test, as TZ would.
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

// Months begin on the 1st and weeks on Monday, both at midnight on the
// manager's clock, and a score counts in the period it was given in.
func TestBoardPeriods(t *testing.T) {
	onClock(t, "UTC")
	ctx := context.Background()
	s := openTemp(t)
	day := func(m time.Month, d, h, min, sec int) time.Time {
		return time.Date(2026, m, d, h, min, sec, 0, time.UTC)
	}
	now := day(10, 14, 12, 0, 0) // a Wednesday
	if day(10, 12, 0, 0, 0).Weekday() != time.Monday {
		t.Fatal("2026-10-12 isn't a Monday")
	}
	for _, c := range []struct {
		period string
		t      time.Time
		want   time.Time
	}{
		{"week", now, day(10, 12, 0, 0, 0)},
		{"week", day(10, 12, 0, 0, 0), day(10, 12, 0, 0, 0)},
		{"week", day(10, 11, 23, 59, 59), day(10, 5, 0, 0, 0)},
		{"week", day(10, 1, 0, 0, 0), day(9, 28, 0, 0, 0)},
		{"month", now, day(10, 1, 0, 0, 0)},
		{"month", day(9, 30, 23, 59, 59), day(9, 1, 0, 0, 0)},
		{"week", now.In(time.FixedZone("UTC+14", 14*3600)), day(10, 12, 0, 0, 0)},
	} {
		if got := PeriodStart(c.period, c.t); got != c.want.Unix() {
			t.Errorf("%s of %s: %s, want %s", c.period, c.t, time.Unix(got, 0).UTC(), c.want)
		}
	}
	// On another clock its midnights count, through the clocks going back
	// (1 November 2026 in Chicago).
	central := onClock(t, "America/Chicago")
	cday := func(m time.Month, d, h int) time.Time { return time.Date(2026, m, d, h, 0, 0, 0, central) }
	for _, c := range []struct {
		period string
		t      time.Time
		want   time.Time
	}{
		{"week", day(10, 12, 4, 0, 0), cday(10, 5, 0)}, // Monday in UTC, Sunday night in Chicago
		{"week", cday(10, 12, 0), cday(10, 12, 0)},
		{"week", cday(11, 1, 12), cday(10, 26, 0)},
		{"week", cday(11, 4, 12), cday(11, 2, 0)},
		{"month", cday(11, 1, 3), cday(11, 1, 0)},
		{"month", day(11, 1, 3, 0, 0), cday(10, 1, 0)},
	} {
		if got := PeriodStart(c.period, c.t); got != c.want.Unix() {
			t.Errorf("Chicago %s of %s: %s, want %s", c.period, c.t, time.Unix(got, 0).In(central), c.want)
		}
	}
	onClock(t, "UTC")

	b := defineBoard(t, s, "wins", KeepSum)
	var list []Score
	for id, at := range map[string]time.Time{
		"sunday-night":  day(10, 11, 23, 59, 59),
		"monday":        day(10, 12, 0, 0, 0),
		"september":     day(9, 30, 23, 59, 59),
		"october-first": day(10, 1, 0, 0, 0),
	} {
		list = append(list, Score{Board: b.ID, Keep: KeepSum, Server: "s1", SteamID: id, Value: 1, At: at.Unix()})
	}
	if err := s.AddScores(ctx, list); err != nil {
		t.Fatal(err)
	}
	for period, want := range map[string]string{
		"all":   "1 september 1, 1 october-first 1, 1 sunday-night 1, 1 monday 1",
		"month": "1 october-first 1, 1 sunday-night 1, 1 monday 1",
		"week":  "1 monday 1",
	} {
		if got := rows(s.Ranking(ctx, BoardQuery{Board: b, Period: period, Now: now})); got != want {
			t.Errorf("%s: %s, want %s", period, got, want)
		}
	}
}

// Playtime is a board of its own, from the visits: each visit counts for
// its part in the period, one going on counts up to now, and the servers'
// add up.
func TestPlaytimeBoard(t *testing.T) {
	onClock(t, "UTC")
	ctx := context.Background()
	s := openTemp(t)
	at := func(d, h int) int64 { return time.Date(2026, 10, d, h, 0, 0, 0, time.UTC).Unix() }
	now := time.Unix(at(14, 12), 0)
	visit := func(server, id string, joined, left int64) {
		t.Helper()
		if err := s.SeenPlayer(ctx, server, id, strings.ToUpper(id), joined); err != nil {
			t.Fatal(err)
		}
		if left > 0 {
			if err := s.LeftPlayer(ctx, server, id, joined, left, "Disconnected."); err != nil {
				t.Fatal(err)
			}
		}
	}
	visit("s1", "a", at(11, 23), at(12, 1)) // Sunday 23:00 to Monday 01:00
	visit("s2", "a", at(13, 10), at(13, 11))
	visit("s1", "b", at(14, 9), 0) // on now, for 3 hours
	visit("s1", "c", at(2, 8), at(2, 9))
	b := Board{ID: PlaytimeBoard, Keep: KeepSum}
	for _, c := range []struct{ server, period, want string }{
		{"", "all", "1 A 10800, 1 B 10800, 3 C 3600"}, // A had it first: B's is still going
		{"", "week", "1 B 10800, 2 A 7200"},
		{"", "month", "1 A 10800, 1 B 10800, 3 C 3600"},
		{"s1", "week", "1 B 10800, 2 A 3600"},
		{"s2", "all", "1 A 3600"},
	} {
		got := rows(s.Ranking(ctx, BoardQuery{Board: b, Server: c.server, Period: c.period, Now: now}))
		if got != c.want {
			t.Errorf("%q %s: %s, want %s", c.server, c.period, got, c.want)
		}
	}
	for _, c := range []struct {
		id, server string
		want       int64
	}{{"a", "", 10800}, {"a", "s2", 3600}, {"b", "", 10800}, {"nobody", "", 0}} {
		if got, err := s.Playtime(ctx, c.id, c.server, now.Unix()); err != nil || got != c.want {
			t.Errorf("%s on %q: %d, %v, want %d", c.id, c.server, got, err, c.want)
		}
	}
	v, err := s.Visits(ctx, "a", 10)
	if err != nil || len(v) != 2 || v[0].Server != "s2" || v[0].LeftAt == nil || *v[0].LeftAt != at(13, 11) || v[1].Server != "s1" {
		t.Errorf("a's visits: %+v, %v", v, err)
	}
	if v, err := s.Visits(ctx, "b", 1); err != nil || len(v) != 1 || v[0].LeftAt != nil {
		t.Errorf("b's visits: %+v, %v", v, err)
	}
}

func TestPluginStore(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	if err := s.SavePluginStore(ctx, "p", map[string][]byte{"a": []byte("1"), "b": []byte("2")}); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePluginStore(ctx, "q", map[string][]byte{"a": []byte("other")}); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePluginStore(ctx, "p", map[string][]byte{"a": nil, "b": []byte("3"), "gone": nil}); err != nil {
		t.Fatal(err)
	}
	got, err := s.PluginStore(ctx, "p")
	if err != nil || len(got) != 1 || string(got["b"]) != "3" {
		t.Errorf("p's store: %q, %v", got, err)
	}
	if got, _ := s.PluginStore(ctx, "q"); string(got["a"]) != "other" {
		t.Errorf("q's store: %q", got)
	}
}

// Pruning forgets the scores of players it forgets, past periods' rows once
// they're old, and nothing of this week's or this month's.
func TestPruneBoards(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	now := time.Now()
	old, recent := now.AddDate(0, 0, -200).Unix(), now.Unix()
	for id, seen := range map[string]int64{"gone": old, "kept": recent} {
		if err := s.SeenPlayer(ctx, "s1", id, id, seen); err != nil {
			t.Fatal(err)
		}
	}
	b := defineBoard(t, s, "wins", KeepSum)
	var list []Score
	for _, id := range []string{"gone", "kept", "never-seen"} {
		list = append(list, Score{Board: b.ID, Keep: KeepSum, Server: "s1", SteamID: id, Value: 1, At: old})
	}
	list = append(list, Score{Board: b.ID, Keep: KeepSum, Server: "s1", SteamID: "kept", Value: 1, At: recent})
	if err := s.AddScores(ctx, list); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PrunePlayers(ctx, now.AddDate(0, 0, -180).Unix()); err != nil {
		t.Fatal(err)
	}
	type row struct {
		Period, SteamID string
		Value           int
	}
	got, err := queryAll(ctx, s.DB, func(r *row) []any { return []any{&r.Period, &r.SteamID, &r.Value} },
		"SELECT period, steam_id, value FROM board_scores ORDER BY steam_id, period")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != "[{all kept 2} {month kept 1} {week kept 1}]" {
		t.Errorf("after pruning: %v", got)
	}
	// Deleting a server takes its scores, and only its.
	if err := s.AddScores(ctx, []Score{{Board: b.ID, Keep: KeepSum, Server: "s2", SteamID: "kept", Value: 1, At: recent}}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteInstance(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	var left string
	s.DB.QueryRow("SELECT group_concat(DISTINCT instance_id) FROM board_scores").Scan(&left)
	if left != "s2" {
		t.Errorf("after deleting s1, scores of %q are left", left)
	}
}
