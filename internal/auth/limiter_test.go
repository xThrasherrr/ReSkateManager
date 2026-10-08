package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLimiter(t *testing.T) {
	l := newLimiter(3, time.Minute)
	now := time.Now()
	for i := range 3 {
		if w := l.wait("k", now); w != 0 {
			t.Fatalf("refused after %d failures", i)
		}
		l.fail("k", now.Add(time.Duration(i)*time.Second))
	}
	if w := l.wait("k", now.Add(3*time.Second)); w != 57*time.Second {
		t.Fatalf("wait = %v, want the first failure's minute to run out", w)
	}
	if w := l.wait("other", now); w != 0 {
		t.Fatal("another key is refused")
	}
	if w := l.wait("k", now.Add(61*time.Second)); w != 0 {
		t.Fatalf("still refused once the first failure is a minute old: %v", w)
	}
}

func TestLimiterBounded(t *testing.T) {
	l := newLimiter(3, time.Minute)
	now := time.Now()
	for i := range limiterKeys + 500 {
		l.fail(strings.Repeat("x", i%7)+string(rune(i)), now)
	}
	if n := len(l.fails); n > limiterKeys {
		t.Fatalf("%d keys kept", n)
	}
}

func TestIPKey(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.9":            "203.0.113.9",
		"::ffff:203.0.113.9":     "203.0.113.9",
		"2001:db8:1:2:3:4:5:6":   "2001:db8:1:2::/64",
		"2001:db8:1:2:ffff::1":   "2001:db8:1:2::/64",
		"not an address at all!": "not an address at all!",
	} {
		if got := ipKey(in); got != want {
			t.Errorf("ipKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCheckPasswordBounds(t *testing.T) {
	good, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword(good, "correct horse battery staple") || CheckPassword(good, "wrong") {
		t.Fatal("a real hash doesn't check out")
	}
	parts := strings.Split(good, "$")
	with := func(i int, v string) string {
		p := append([]string(nil), parts...)
		p[i] = v
		return strings.Join(p, "$")
	}
	for name, h := range map[string]string{
		"no lanes":      with(3, "m=32768,t=2,p=0"),
		"no passes":     with(3, "m=32768,t=0,p=1"),
		"a lot of RAM":  with(3, "m=4194304,t=2,p=1"),
		"many passes":   with(3, "m=32768,t=1000,p=1"),
		"other version": with(2, "v=16"),
		"short salt":    with(4, "c2FsdA"),
		"empty key":     with(5, ""),
		"argon2i":       with(1, "argon2i"),
	} {
		if CheckPassword(h, "correct horse battery staple") {
			t.Errorf("%s: accepted", name)
		}
	}
}

func newService(t *testing.T) *Service {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(t.TempDir(), "a.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, q := range []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT NOT NULL UNIQUE COLLATE NOCASE, password_hash TEXT,
			steam_id TEXT UNIQUE, is_owner INTEGER NOT NULL DEFAULT 0, disabled INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL, last_login_at INTEGER)`,
		`CREATE TABLE roles (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, permissions TEXT NOT NULL, builtin INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE user_roles (user_id INTEGER NOT NULL, role_id INTEGER NOT NULL, instance_id TEXT NOT NULL DEFAULT '*',
			PRIMARY KEY (user_id, role_id, instance_id))`,
		`CREATE TABLE sessions (token_hash TEXT PRIMARY KEY, user_id INTEGER NOT NULL, created_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL, last_seen_at INTEGER NOT NULL, ip TEXT, user_agent TEXT)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	s, err := New(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestLoginLimits(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	const pw = "correct horse battery staple"
	for _, name := range []string{"owner", "mallory"} {
		if _, err := s.CreateUser(ctx, NewUser{Username: name, Password: pw, Owner: name == "owner"}); err != nil {
			t.Fatal(err)
		}
	}

	// Signing in to one's own account between guesses earns no more of them.
	refused := 0
	for range 30 {
		_, err := s.Login(ctx, "198.51.100.7", "owner", "guess")
		if _, many := errors.AsType[*TooManyAttemptsError](err); many {
			refused++
		}
		if _, err := s.Login(ctx, "198.51.100.7", "mallory", pw); err != nil {
			if _, many := errors.AsType[*TooManyAttemptsError](err); !many {
				t.Fatal(err)
			}
		}
	}
	if refused < 20 {
		t.Fatalf("only %d of 30 guesses refused from one address", refused)
	}

	// Many addresses share the account's budget.
	s = newService(t)
	if _, err := s.CreateUser(ctx, NewUser{Username: "owner", Password: pw, Owner: true}); err != nil {
		t.Fatal(err)
	}
	refused = 0
	for i := range 40 {
		ip := fmt.Sprintf("198.51.100.%d", i)
		if _, err := s.Login(ctx, ip, "owner", "guess"); err != nil {
			if _, many := errors.AsType[*TooManyAttemptsError](err); many {
				refused++
			}
		}
	}
	if refused != 20 {
		t.Fatalf("%d of 40 guesses from 40 addresses refused, want 20", refused)
	}
	// While the account is limited, even its owner waits (or signs in with Steam).
	if _, err := s.Login(ctx, "192.0.2.1", "owner", pw); err == nil {
		t.Fatal("signed in while the account is limited")
	}

	// Unknown names count against the address only.
	s = newService(t)
	for range 10 {
		s.Login(ctx, "192.0.2.50", "nobody", "guess")
	}
	if _, err := s.Login(ctx, "192.0.2.50", "nobody", "guess"); err == nil {
		t.Fatal("no error")
	} else if _, many := errors.AsType[*TooManyAttemptsError](err); !many {
		t.Fatalf("11th guess from one address: %v", err)
	}
}

func TestSessionDoesNotSlide(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	u, err := s.CreateUser(ctx, NewUser{Username: "owner", Password: "correct horse battery staple", Owner: true})
	if err != nil {
		t.Fatal(err)
	}
	token, err := s.NewSession(ctx, u.ID, "127.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	// Used now and then, it still ends SessionTTL after it began.
	if _, err := s.db.Exec("UPDATE sessions SET last_seen_at = 0, expires_at = ?", time.Now().Add(time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Session(ctx, token); err != nil {
		t.Fatal(err)
	}
	var expires int64
	if err := s.db.QueryRow("SELECT expires_at FROM sessions").Scan(&expires); err != nil {
		t.Fatal(err)
	}
	if expires > time.Now().Add(2*time.Minute).Unix() {
		t.Fatal("using a session pushed its end back")
	}
}

func TestRelinkingSteamEndsSessions(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	u, err := s.CreateUser(ctx, NewUser{Username: "owner", Password: "correct horse battery staple", SteamID: "76561198000000001", Owner: true})
	if err != nil {
		t.Fatal(err)
	}
	sessions := func() (n int) {
		s.db.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&n)
		return n
	}
	s.NewSession(ctx, u.ID, "", "")
	same := "76561198000000001"
	if _, err := s.UpdateUser(ctx, u.ID, UserUpdate{SteamID: &same}); err != nil || sessions() != 1 {
		t.Fatalf("the same account again: %v, %d sessions", err, sessions())
	}
	other := "76561198000000002"
	if _, err := s.UpdateUser(ctx, u.ID, UserUpdate{SteamID: &other}); err != nil || sessions() != 0 {
		t.Fatalf("another account: %v, %d sessions", err, sessions())
	}
	if _, err := s.CreateUser(ctx, NewUser{Username: "mod", SteamID: "76561198000000003"}); err != nil {
		t.Fatal(err)
	}
	taken := "76561198000000003"
	if _, err := s.UpdateUser(ctx, u.ID, UserUpdate{SteamID: &taken}); !errors.Is(err, ErrSteamTaken) {
		t.Fatalf("someone else's account: %v", err)
	}
}

func TestSetupLimitedPerAddress(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	for range 10 {
		s.Setup(ctx, "198.51.100.7", "00000000", "owner", "correct horse battery staple")
	}
	if _, err := s.Setup(ctx, "198.51.100.7", s.SetupPIN(), "owner", "correct horse battery staple"); err == nil {
		t.Fatal("a flooding address set the panel up")
	}
	// Someone else's wrong PINs don't lock the owner out.
	if _, err := s.Setup(ctx, "192.0.2.1", s.SetupPIN(), "owner", "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Setup(ctx, "192.0.2.1", s.SetupPIN(), "owner2", "correct horse battery staple"); !errors.Is(err, ErrSetUp) {
		t.Fatalf("second setup: %v", err)
	}
}
