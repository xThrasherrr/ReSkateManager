package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestAnnouncements(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	a := Announcement{Instance: "a", Message: "join the discord", Interval: 600, Enabled: true}
	all := Announcement{Instance: AllInstances, Message: "everywhere", Interval: 3600, Enabled: true}
	b := Announcement{Instance: "b", Message: "other", Interval: 60}
	for _, x := range []*Announcement{&a, &all, &b} {
		if err := s.SaveAnnouncement(ctx, x); err != nil || x.ID == 0 {
			t.Fatalf("save %+v: %v", x, err)
		}
	}
	got, err := s.Announcements(ctx, "a")
	if err != nil || len(got) != 2 || got[0].ID != a.ID || got[1].ID != all.ID {
		t.Fatalf("Announcements(a) = %+v, %v", got, err)
	}
	if got, _ := s.Announcements(ctx, ""); len(got) != 3 {
		t.Fatalf("Announcements() has %d", len(got))
	}

	a.Enabled, a.Interval = false, 120
	if err := s.SaveAnnouncement(ctx, &a); err != nil {
		t.Fatal(err)
	}
	if x, err := s.Announcement(ctx, a.ID); err != nil || x.Enabled || x.Interval != 120 {
		t.Fatalf("after update = %+v, %v", x, err)
	}

	if err := s.DeleteInstance(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Announcement(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("server's announcement kept after removing it: %v", err)
	}
	if _, err := s.Announcement(ctx, all.ID); err != nil {
		t.Fatalf("every-server announcement removed with one server: %v", err)
	}
}

// A panel set up before announcements existed gets the permission on its
// built-in Administrator role.
func TestAnnouncementsMigrationGrantsAdministrator(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"CREATE TABLE roles (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE COLLATE NOCASE, permissions TEXT NOT NULL DEFAULT '[]', builtin INTEGER NOT NULL DEFAULT 0)",
		"CREATE TABLE instances (id TEXT PRIMARY KEY, name TEXT NOT NULL, dir TEXT NOT NULL UNIQUE, auto_start INTEGER NOT NULL DEFAULT 0, auto_restart INTEGER NOT NULL DEFAULT 1, auto_update INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL)",
		`INSERT INTO roles(name, permissions, builtin) VALUES('Administrator', '["console.view"]', 1), ('Moderator', '["console.view"]', 1)`,
		"PRAGMA user_version = 3",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var admin, mod string
	s.DB.QueryRow("SELECT permissions FROM roles WHERE name = 'Administrator'").Scan(&admin)
	s.DB.QueryRow("SELECT permissions FROM roles WHERE name = 'Moderator'").Scan(&mod)
	if admin != `["console.view","announcements.manage"]` {
		t.Fatalf("Administrator = %s", admin)
	}
	if strings.Contains(mod, "announcements") {
		t.Fatalf("Moderator = %s", mod)
	}
}
