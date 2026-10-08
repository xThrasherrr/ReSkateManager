// Package housekeep keeps what the manager stores from growing forever: old
// audit log entries, players not seen for a long time, and server releases
// no install needs any more. Performance samples prune themselves (perf).
package housekeep

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/store"
	"github.com/xThrasherrr/ReSkateManager/internal/updater"
)

// Retention is how many days the audit log and player history are kept. 0
// keeps them for good.
type Retention struct {
	AuditDays  int `json:"auditDays"`
	PlayerDays int `json:"playerDays"`
}

// DefaultRetention is what a manager keeps until someone changes it.
var DefaultRetention = Retention{AuditDays: 90, PlayerDays: 180}

// MaxDays bounds a retention setting: ten years, past which 0 says it better.
const MaxDays = 3650

// Releases is how many server release archives the cache keeps.
const Releases = 2

const retentionKey = "retention"

// LoadRetention reads the retention, or the default when none is saved.
func LoadRetention(ctx context.Context, s *store.Store) (Retention, error) {
	v, err := s.Get(ctx, retentionKey)
	if errors.Is(err, store.ErrNotFound) {
		return DefaultRetention, nil
	}
	if err != nil {
		return DefaultRetention, err
	}
	r := DefaultRetention
	err = json.Unmarshal([]byte(v), &r)
	return r, err
}

// SaveRetention checks and stores the retention.
func SaveRetention(ctx context.Context, s *store.Store, r Retention) error {
	for _, d := range []int{r.AuditDays, r.PlayerDays} {
		if d < 0 || d > MaxDays {
			return fmt.Errorf("keep things 1 to %d days, or 0 for good", MaxDays)
		}
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return s.Set(ctx, retentionKey, string(b))
}

// Keeper prunes what would otherwise grow forever: the audit log, player
// history, and old server releases in the cache.
type Keeper struct {
	Store    *store.Store
	CacheDir string
	Log      *slog.Logger
}

// Run sweeps now, then every hour until ctx ends.
func (k *Keeper) Run(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		k.Sweep(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Sweep drops what the retention no longer keeps, as of now.
func (k *Keeper) Sweep(ctx context.Context, now time.Time) {
	r, err := LoadRetention(ctx, k.Store)
	if err != nil {
		k.Log.Warn("retention settings", "err", err)
	}
	before := func(days int) int64 { return now.AddDate(0, 0, -days).Unix() }
	if r.AuditDays > 0 {
		if n, err := k.Store.PruneAudit(ctx, before(r.AuditDays)); err != nil && ctx.Err() == nil {
			k.Log.Warn("prune audit log", "err", err)
		} else if n > 0 {
			k.Log.Info("pruned the audit log", "entries", n, "days", r.AuditDays)
		}
	}
	if r.PlayerDays > 0 {
		if n, err := k.Store.PrunePlayers(ctx, before(r.PlayerDays)); err != nil && ctx.Err() == nil {
			k.Log.Warn("prune player history", "err", err)
		} else if n > 0 {
			k.Log.Info("pruned player history", "players", n, "days", r.PlayerDays)
		}
	}
	if k.CacheDir != "" {
		gone, err := updater.PruneCache(k.CacheDir, Releases)
		if err != nil {
			k.Log.Warn("prune server releases", "err", err)
		}
		for _, name := range gone {
			k.Log.Info("deleted an old server release", "file", name)
		}
	}
}
