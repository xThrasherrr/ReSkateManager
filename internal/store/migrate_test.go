package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// openAt makes a database migrated only up to version n.
func openAt(t *testing.T, n int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "manager.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	list, err := migrationList()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range list[:n] {
		body, _ := migrations.ReadFile(m.name)
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", n)); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBeforeMigrate(t *testing.T) {
	list, _ := migrationList()
	latest := list[len(list)-1].n
	path := openAt(t, latest-1)

	// A failing hook leaves the database as it was.
	if _, err := OpenWith(path, func(*sql.DB, int) error { return errors.New("disk full") }); err == nil {
		t.Fatal("opened although the backup failed")
	}
	var from []int
	hook := func(db *sql.DB, v int) error {
		from = append(from, v)
		return nil
	}
	s, err := OpenWith(path, hook)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := Schema(s.DB); v != latest || len(from) != 1 || from[0] != latest-1 {
		t.Errorf("schema %d after a hook that saw %v", v, from)
	}
	s.Close()

	// Up to date, and new: nothing to back up.
	if s, err = OpenWith(path, hook); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if s, err = OpenWith(filepath.Join(t.TempDir(), "new.db"), hook); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if len(from) != 1 {
		t.Errorf("the hook ran %d times", len(from))
	}
}

// A folder named with characters a URI gives meaning to still opens.
func TestOpenOddPaths(t *testing.T) {
	for _, name := range []string{"a#b", "c?d", "e%20f", "g h", "Ünï"} {
		dir := filepath.Join(t.TempDir(), name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			continue // not a name this OS allows, as "?" on Windows
		}
		st, err := Open(filepath.Join(dir, "manager.db"))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		st.Close()
		if _, err := os.Stat(filepath.Join(dir, "manager.db")); err != nil {
			t.Errorf("%s: the database went elsewhere: %v", name, err)
		}
	}
}

// A database from a newer manager isn't opened: this one would misread it.
func TestRefusesNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manager.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	latest, _ := Latest()
	if _, err := st.DB.Exec(fmt.Sprintf("PRAGMA user_version = %d", latest+1)); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if st, err := Open(path); err == nil {
		st.Close()
		t.Fatal("opened a newer schema")
	}
}
