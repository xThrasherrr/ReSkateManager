package housekeep

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/store"
)

func TestSweepKeepsWhatRetentionSays(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	day := func(n int) int64 { return now.AddDate(0, 0, -n).Unix() }
	for _, at := range []int64{day(100), day(10)} {
		s.Audit(ctx, store.AuditEntry{At: at, Action: "x"})
	}
	s.SeenPlayer(ctx, "a", "76561198000000001", "old", day(201))
	s.LeftPlayer(ctx, "a", "76561198000000001", day(201), day(200), "Disconnected.")
	s.SeenPlayer(ctx, "a", "76561198000000002", "recent", day(300))
	s.LeftPlayer(ctx, "a", "76561198000000002", day(300), day(300)+60, "Disconnected.")
	s.SeenPlayer(ctx, "a", "76561198000000002", "recent", day(1))

	if r, err := LoadRetention(ctx, s); err != nil || r != DefaultRetention {
		t.Fatalf("default retention: %+v, %v", r, err)
	}
	k := &Keeper{Store: s, Log: slog.New(slog.DiscardHandler)}
	k.Sweep(ctx, now)
	if log, _ := s.AuditLog(ctx, "", 0, 10); len(log) != 1 || log[0].At != day(10) {
		t.Errorf("audit log after 90 days: %+v", log)
	}
	if players, _ := s.PlayerHistory(ctx, "a", "", 10); len(players) != 1 || players[0].Name != "recent" {
		t.Errorf("players after 180 days: %+v", players)
	}
	// A player kept keeps all their visits, however old; one forgotten takes theirs along.
	var visits int
	s.DB.QueryRow("SELECT COUNT(*) FROM player_sessions WHERE steam_id = '76561198000000002'").Scan(&visits)
	if visits != 2 {
		t.Errorf("the kept player has %d visits, want 2", visits)
	}
	s.DB.QueryRow("SELECT COUNT(*) FROM player_sessions WHERE steam_id = '76561198000000001'").Scan(&visits)
	if visits != 0 {
		t.Errorf("the forgotten player has %d visits left", visits)
	}

	// 0 keeps them for good; a bad value is refused.
	if err := SaveRetention(ctx, s, Retention{AuditDays: 0, PlayerDays: 5}); err != nil {
		t.Fatal(err)
	}
	s.Audit(ctx, store.AuditEntry{At: day(1000), Action: "ancient"})
	k.Sweep(ctx, now)
	if log, _ := s.AuditLog(ctx, "", 0, 10); len(log) != 2 {
		t.Errorf("audit log kept for good: %+v", log)
	}
	if err := SaveRetention(ctx, s, Retention{AuditDays: -1}); err == nil {
		t.Error("saved negative days")
	}

	// The release cache keeps the newest two.
	k.CacheDir = t.TempDir()
	for i, name := range []string{"ReSkateServer-1.zip", "ReSkateServer-2.zip", "ReSkateServer-3.zip"} {
		p := filepath.Join(k.CacheDir, name)
		os.WriteFile(p, []byte("x"), 0o644)
		at := now.Add(-time.Duration(3-i) * time.Hour)
		os.Chtimes(p, at, at)
	}
	k.Sweep(ctx, now)
	if _, err := os.Stat(filepath.Join(k.CacheDir, "ReSkateServer-1.zip")); !os.IsNotExist(err) {
		t.Error("the oldest release survived")
	}
}
