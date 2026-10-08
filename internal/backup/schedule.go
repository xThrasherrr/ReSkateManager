package backup

import (
	"context"
	"time"
)

// Run makes the scheduled backups until ctx ends. onFail hears of one that
// failed; it's tried again an hour later. The schedule counts from the
// newest scheduled backup in Dir, so one missed while the manager was off is
// made soon after it starts.
func (s *Service) Run(ctx context.Context, onFail func(error)) {
	var last, failed time.Time
	if list, err := s.List(); err == nil {
		for _, b := range list {
			if b.Kind == Scheduled {
				last = time.Unix(b.Created, 0)
				break
			}
		}
	}
	s.prune(ctx) // a migration backup made while starting is past keep, maybe
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		set, err := LoadSettings(ctx, s.Store)
		if err != nil {
			s.Log.Warn("backup settings", "err", err)
			continue
		}
		now := time.Now()
		if !due(set, last, failed, now) {
			continue
		}
		info, err := s.Create(ctx, Options{Kind: Scheduled, Database: true, Servers: s.serverIDs(), Mods: set.Mods})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			failed = now
			s.Log.Warn("scheduled backup failed", "err", err)
			onFail(err)
			continue
		}
		last, failed = time.Unix(info.Created, 0), time.Time{}
		s.Log.Info("scheduled backup made", "file", info.Name, "bytes", info.Size)
	}
}

// due reports whether a scheduled backup is due at now: every so many hours
// after the last one, and not within an hour of one that failed.
func due(set Settings, last, failed, now time.Time) bool {
	if set.Every <= 0 {
		return false
	}
	if !failed.IsZero() && now.Before(failed.Add(time.Hour)) {
		return false
	}
	return !now.Before(last.Add(time.Duration(set.Every) * time.Hour))
}

// prune keeps the automatic backups to the number the settings say.
func (s *Service) prune(ctx context.Context) {
	set, err := LoadSettings(ctx, s.Store)
	if err != nil {
		s.Log.Warn("backup settings", "err", err)
	}
	gone, err := s.Prune(set.Keep)
	if err != nil {
		s.Log.Warn("delete old backups", "err", err)
	}
	for _, name := range gone {
		s.Log.Info("deleted an old backup", "file", name)
	}
}

func (s *Service) serverIDs() []string { return s.Reg.IDs() }
