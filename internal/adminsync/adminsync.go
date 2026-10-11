// Package adminsync keeps each server's in-game admins in step with the panel
// users who hold ingame.admin on it and have a linked Steam account.
package adminsync

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/auth"
	"github.com/xThrasherrr/ReSkateManager/internal/instance"
	"github.com/xThrasherrr/ReSkateManager/internal/serverconfig"
	"github.com/xThrasherrr/ReSkateManager/internal/store"
)

// Syncer keeps in-game admins in step with the panel; nil does nothing.
type Syncer struct {
	Store *store.Store
	Auth  *auth.Service
	Reg   *instance.Registry
	Log   *slog.Logger

	mu sync.Mutex // one sync at a time, so managed_admins stays consistent
}

// Kick syncs every server in the background, after users or roles change.
// Each gets its own time, so one slow server can't run the others out of it.
func (s *Syncer) Kick() {
	if s == nil {
		return
	}
	go func() {
		for _, in := range s.Reg.List() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := s.Sync(ctx, in); err != nil {
				s.Log.Warn("in-game admin sync", "instance", in.ID(), "err", err)
			}
			cancel()
		}
	}()
}

// Sync brings one server's admins in line with the panel. A running server is
// sent admin commands (role commands on a server with roles) and a stopped one
// has its config or roles edited. One that is starting or updating is
// skipped: it syncs when it becomes ready.
func (s *Syncer) Sync(ctx context.Context, in *instance.Instance) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	panel, err := s.PanelAdmins(ctx, in.ID())
	if err != nil {
		return err
	}
	// Only SteamIDs: the server takes anything else after "admin add" or
	// "role" as the start of a player's name.
	want := make([]string, 0, len(panel))
	for id := range panel {
		if serverconfig.SteamIDRe.MatchString(id) {
			want = append(want, id)
		}
	}
	managed, err := s.Store.ManagedAdmins(ctx, in.ID())
	if err != nil {
		return err
	}
	managed = slices.DeleteFunc(managed, func(id string) bool { return !serverconfig.SteamIDRe.MatchString(id) })
	path := in.Def().ConfigPath()

	if in.State() == instance.Running {
		// The server rewrites its config (its roles, from 2.0.6) after every
		// admin command, so the file is its current list.
		f, err := serverconfig.Read(path)
		if err != nil {
			return err
		}
		add, remove, next := Plan(f.Admins(), want, managed)
		var failed error
		for _, id := range add {
			// Kept as ours even when it failed: no reply doesn't mean the
			// server didn't do it, and an admin the manager added but lost
			// track of would never be taken away again.
			if _, err := in.Command(ctx, AddCommand(f, id), "panel"); err != nil {
				failed = errors.Join(failed, err)
			}
		}
		for _, id := range remove {
			if _, err := in.Command(ctx, RemoveCommand(f, id), "panel"); err != nil {
				next = append(next, id) // still ours; try again next time
				failed = errors.Join(failed, err)
			}
		}
		// Recorded even when the commands ran out of time, which is when it
		// matters most.
		return errors.Join(failed, s.Store.SetManagedAdmins(context.WithoutCancel(ctx), in.ID(), next))
	}

	var next []string
	ran, err := in.IfStopped(func() error {
		f, err := serverconfig.Read(path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil // never configured; nothing to keep in step yet
		}
		if err != nil {
			return err
		}
		current := f.Admins()
		add, remove, n := Plan(current, want, managed)
		next = n
		if len(add) == 0 && len(remove) == 0 {
			return nil
		}
		list := slices.DeleteFunc(current, func(x string) bool { return slices.Contains(remove, x) })
		if err := f.SetAdmins(append(list, add...)); err != nil {
			return err
		}
		return f.Write(path)
	})
	if !ran || err != nil || next == nil {
		return err
	}
	return s.Store.SetManagedAdmins(ctx, in.ID(), next)
}

// AddCommand is the console command that makes id an admin on a running
// server whose config is f: from ReSkate 2.0.6, by giving them the admin role.
func AddCommand(f *serverconfig.File, id string) string {
	if f.HasRoles() {
		return "role " + id + " " + serverconfig.AdminRole
	}
	return "admin add " + id
}

// RemoveCommand is the console command that takes an admin's place away: from
// ReSkate 2.0.6, their role, which leaves them the default one.
func RemoveCommand(f *serverconfig.File, id string) string {
	if f.HasRoles() {
		return "role " + id + " none"
	}
	return "admin remove " + id
}

// PanelAdmins maps the Steam IDs that should be in-game admins on an instance
// to the panel users they belong to.
func (s *Syncer) PanelAdmins(ctx context.Context, instanceID string) (map[string]string, error) {
	out := map[string]string{}
	if s == nil {
		return out, nil
	}
	users, err := s.Auth.Users(ctx)
	if err != nil {
		return nil, err
	}
	for _, u := range users {
		if u.Disabled || u.SteamID == "" {
			continue
		}
		p, err := s.Auth.Perms(ctx, u)
		if err != nil {
			return nil, err
		}
		if p.Can(auth.IngameAdmin, instanceID) {
			out[u.SteamID] = u.Username
		}
	}
	return out, nil
}

// Plan works out what to change on a server whose admins are current, given
// the panel's wanted admins and the ones the manager added before (managed).
// It only removes admins it added, and returns the new managed list.
func Plan(current, want, managed []string) (add, remove, next []string) {
	for _, id := range want {
		if !slices.Contains(current, id) {
			add = append(add, id)
		}
		if slices.Contains(managed, id) || !slices.Contains(current, id) {
			next = append(next, id)
		}
	}
	for _, id := range managed {
		if slices.Contains(current, id) && !slices.Contains(want, id) {
			remove = append(remove, id)
		}
	}
	slices.Sort(add)
	slices.Sort(remove)
	slices.Sort(next)
	if next == nil {
		next = []string{}
	}
	return add, remove, next
}
