package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

type visit struct {
	Joined int64
	Left   *int64
	Reason string
}

func visitsOf(t *testing.T, s *Store, instanceID, steamID string) []visit {
	t.Helper()
	v, err := queryAll(context.Background(), s.DB, func(v *visit) []any { return []any{&v.Joined, &v.Left, &v.Reason} },
		"SELECT joined_at, left_at, reason FROM player_sessions WHERE instance_id = ? AND steam_id = ? ORDER BY joined_at", instanceID, steamID)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// A join and its leave are written from goroutines of their own, so either
// can land first; both orders make one closed visit.
func TestVisitEitherOrder(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	const id = "76561198000000001"
	if err := s.SeenPlayer(ctx, "a", id, "Alice", 1000); err != nil {
		t.Fatal(err)
	}
	if v := visitsOf(t, s, "a", id); len(v) != 1 || v[0].Left != nil {
		t.Fatalf("after the join: %+v", v)
	}
	if err := s.LeftPlayer(ctx, "a", id, 1000, 1600, "Disconnected."); err != nil {
		t.Fatal(err)
	}
	if err := s.LeftPlayer(ctx, "a", id, 2000, 2500, "Kicked."); err != nil {
		t.Fatal(err)
	}
	if err := s.SeenPlayer(ctx, "a", id, "Alice", 2000); err != nil {
		t.Fatal(err)
	}
	v := visitsOf(t, s, "a", id)
	if len(v) != 2 || v[0].Left == nil || *v[0].Left != 1600 || v[0].Reason != "Disconnected." ||
		v[1].Left == nil || *v[1].Left != 2500 || v[1].Reason != "Kicked." {
		t.Fatalf("visits %+v", v)
	}
	h, err := s.PlayerHistory(ctx, "a", "", 10)
	if err != nil || len(h) != 1 || h[0].Sessions != 2 || h[0].FirstSeen != 1000 || h[0].LastSeen != 2500 {
		t.Fatalf("history %+v, %v", h, err)
	}
	if h[0].Playtime == nil || *h[0].Playtime != 600+500 {
		t.Errorf("playtime %v, want 1100", h[0].Playtime)
	}
}

// Playtime is per server, with the visit going on now counted to now, and
// adds up across the servers asked for. Players from before visits were kept
// have none.
func TestPlaytime(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	const alice, bob = "76561198000000001", "76561198000000002"
	now := time.Now().Unix()
	s.SeenPlayer(ctx, "a", alice, "Alice", now-1000)
	s.LeftPlayer(ctx, "a", alice, now-1000, now-900, "Disconnected.")
	s.SeenPlayer(ctx, "a", alice, "Alice", now-50) // still on
	s.SeenPlayer(ctx, "b", alice, "Alice", now-5000)
	s.LeftPlayer(ctx, "b", alice, now-5000, now-4000, "Disconnected.")
	s.SeenPlayer(ctx, "c", alice, "Alice", now-9000)
	s.LeftPlayer(ctx, "c", alice, now-9000, now-8000, "Disconnected.")
	s.DB.Exec("INSERT INTO player_history(instance_id, steam_id, name, first_seen, last_seen, sessions) VALUES('a', ?, 'Bob', 1, 2, 3)", bob)

	within := func(got *int64, want int64) bool { return got != nil && *got >= want && *got <= want+5 }
	h, err := s.PlayerHistory(ctx, "a", "", 10)
	if err != nil || len(h) != 2 || h[0].SteamID != alice {
		t.Fatalf("history %+v, %v", h, err)
	}
	if !within(h[0].Playtime, 150) || h[0].PlaytimeAll != nil {
		t.Errorf("on a alone: %v, all %v", h[0].Playtime, h[0].PlaytimeAll)
	}
	if h[1].Playtime != nil {
		t.Errorf("Bob, from before visits were kept, has playtime %d", *h[1].Playtime)
	}
	if h, _ = s.PlayerHistory(ctx, "a", "", 10, "a", "b"); !within(h[0].PlaytimeAll, 1150) || h[1].PlaytimeAll != nil {
		t.Errorf("on a and b: %v; Bob %v", h[0].PlaytimeAll, h[1].PlaytimeAll)
	}
	if h, _ = s.PlayerHistory(ctx, "a", "Ali", 10, "a", "b", "c"); len(h) != 1 || !within(h[0].PlaytimeAll, 2150) {
		t.Errorf("searched, on all three: %+v", h)
	}
}

// A visit the last run never closed ends at its server's last sample since
// the join, or at the join without one; closed ones are left alone.
func TestCloseOpenSessions(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	const alice, bob = "76561198000000001", "76561198000000002"
	s.SeenPlayer(ctx, "a", alice, "Alice", 1000)
	s.SeenPlayer(ctx, "a", bob, "Bob", 500)
	s.LeftPlayer(ctx, "a", bob, 500, 700, "Disconnected.")
	s.SeenPlayer(ctx, "b", bob, "Bob", 2000)
	for _, at := range []int64{900, 1030, 1060} {
		s.AddPerfSample(ctx, "a", PerfSample{At: at, Run: 800})
	}
	s.AddPerfSample(ctx, "b", PerfSample{At: 1500, Run: 1400}) // before Bob joined b

	n, err := s.CloseOpenSessions(ctx, "Manager stopped.")
	if err != nil || n != 2 {
		t.Fatalf("closed %d, %v", n, err)
	}
	if v := visitsOf(t, s, "a", alice); len(v) != 1 || v[0].Left == nil || *v[0].Left != 1060 || v[0].Reason != "Manager stopped." {
		t.Errorf("Alice on a: %+v", v)
	}
	if v := visitsOf(t, s, "a", bob); len(v) != 1 || *v[0].Left != 700 || v[0].Reason != "Disconnected." {
		t.Errorf("Bob's closed visit changed: %+v", v)
	}
	if v := visitsOf(t, s, "b", bob); len(v) != 1 || v[0].Left == nil || *v[0].Left != 2000 {
		t.Errorf("Bob on b: %+v", v)
	}
	if h, _ := s.PlayerHistory(ctx, "a", alice, 1); len(h) != 1 || h[0].LastSeen != 1060 {
		t.Errorf("Alice last seen %+v", h)
	}
	if n, err := s.CloseOpenSessions(ctx, "Manager stopped."); n != 0 || err != nil {
		t.Errorf("closed %d again, %v", n, err)
	}
}

// Removing a server takes its visits; other servers keep theirs.
func TestDeleteInstanceVisits(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	const alice = "76561198000000001"
	s.SeenPlayer(ctx, "a", alice, "Alice", 1000)
	s.SeenPlayer(ctx, "b", alice, "Alice", 1000)
	if err := s.DeleteInstance(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if v := visitsOf(t, s, "a", alice); len(v) != 0 {
		t.Errorf("a's visits kept: %+v", v)
	}
	if v := visitsOf(t, s, "b", alice); len(v) != 1 {
		t.Errorf("b's visits: %+v", v)
	}
}

// A database from before visits were kept upgrades with its history whole.
func TestUpgradeKeepsHistory(t *testing.T) {
	path := openAt(t, 10)
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = old.Exec("INSERT INTO player_history(instance_id, steam_id, name, first_seen, last_seen, sessions) VALUES('a', '76561198000000001', 'Alice', 10, 20, 4)")
	old.Close()
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, _ := Schema(s.DB); v < 11 {
		t.Errorf("schema %d, want 011's visits", v)
	}
	h, err := s.PlayerHistory(context.Background(), "a", "", 10, "a", "b")
	if err != nil || len(h) != 1 || h[0].Name != "Alice" || h[0].FirstSeen != 10 || h[0].LastSeen != 20 || h[0].Sessions != 4 ||
		h[0].Playtime != nil || h[0].PlaytimeAll != nil {
		t.Fatalf("history after the upgrade %+v, %v", h, err)
	}
	// Their next visit counts from there.
	if err := s.SeenPlayer(context.Background(), "a", "76561198000000001", "Alice", 100); err != nil {
		t.Fatal(err)
	}
	if h, _ := s.PlayerHistory(context.Background(), "a", "", 10); h[0].Sessions != 5 || h[0].FirstSeen != 10 || h[0].Playtime == nil {
		t.Errorf("after a visit %+v", h)
	}
}
