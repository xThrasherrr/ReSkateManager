// Package store is the manager's SQLite database.
package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"

	"github.com/xThrasherrr/ReSkateManager/internal/instance"
	"github.com/xThrasherrr/ReSkateManager/internal/logparse"
)

//go:embed migrations/*.sql
var migrations embed.FS

// ErrNotFound is a row that isn't there.
var ErrNotFound = errors.New("not found")

// Store is the manager's database. DB is its one writer, and Read reads on
// connections of its own, for reads that can take a while.
type Store struct{ DB, Read *sql.DB }

// Open opens the database at path, made and migrated as needed.
func Open(path string) (*Store, error) { return OpenWith(path, nil) }

// OpenWith is Open, with beforeMigrate run once before a database that has
// a schema already (version from) is migrated to a newer one, so it can be
// backed up first. An error from it leaves the database as it was.
func OpenWith(path string, beforeMigrate func(db *sql.DB, from int) error) (*Store, error) {
	db, err := sql.Open("sqlite", DSN(path, "foreign_keys(1)", "journal_mode(WAL)", "busy_timeout(5000)", "synchronous(NORMAL)"))
	if err != nil {
		return nil, err
	}
	// One writer keeps SQLite free of "database is locked" under concurrent requests.
	db.SetMaxOpenConns(1)
	s := &Store{DB: db}
	if err := s.migrate(beforeMigrate); err != nil {
		db.Close()
		return nil, err
	}
	// WAL lets these read while the writer writes.
	if s.Read, err = sql.Open("sqlite", DSN(path, "query_only(1)", "busy_timeout(5000)")); err != nil {
		db.Close()
		return nil, err
	}
	s.Read.SetMaxOpenConns(4)
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return errors.Join(s.Read.Close(), s.DB.Close()) }

// DSN is the address of the database file at path for the sqlite driver,
// with these pragmas. It is a URI, so a folder named with "#", "?" or "%"
// must be escaped, or SQLite reads part of the path as the query.
func DSN(path string, pragmas ...string) string {
	q := url.Values{}
	for _, p := range pragmas {
		q.Add("_pragma", p)
	}
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p // C:/... becomes /C:/..., as a file: URI writes it
	}
	return (&url.URL{Scheme: "file", Path: p, RawQuery: q.Encode()}).String()
}

// Latest is the newest schema version this manager knows.
func Latest() (int, error) {
	list, err := migrationList()
	if err != nil || len(list) == 0 {
		return 0, err
	}
	return list[len(list)-1].n, nil
}

// Schema is the schema version of the database at hand.
func Schema(db *sql.DB) (int, error) {
	var version int
	err := db.QueryRow("PRAGMA user_version").Scan(&version)
	return version, err
}

type migration struct {
	n    int
	name string
}

func migrationList() ([]migration, error) {
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return nil, err
	}
	slices.Sort(names)
	out := make([]migration, 0, len(names))
	for _, name := range names {
		base := strings.TrimPrefix(name, "migrations/")
		n, err := strconv.Atoi(strings.SplitN(base, "_", 2)[0])
		if err != nil {
			return nil, fmt.Errorf("migration %s: bad name", name)
		}
		out = append(out, migration{n, name})
	}
	return out, nil
}

func (s *Store) migrate(beforeMigrate func(db *sql.DB, from int) error) error {
	version, err := Schema(s.DB)
	if err != nil {
		return err
	}
	list, err := migrationList()
	if err != nil {
		return err
	}
	// Made by a newer manager: this one would misread what it doesn't know.
	if latest := list[len(list)-1].n; version > latest {
		return fmt.Errorf("the database is from a newer manager (schema %d; this one knows up to %d); run that version, or restore a backup made with this one", version, latest)
	}
	if beforeMigrate != nil && version > 0 && len(list) > 0 && version < list[len(list)-1].n {
		if err := beforeMigrate(s.DB, version); err != nil {
			return fmt.Errorf("back up the database before upgrading it: %w", err)
		}
	}
	for _, m := range list {
		n, name := m.n, m.name
		if n <= version {
			continue
		}
		body, err := migrations.ReadFile(name)
		if err != nil {
			return err
		}
		tx, err := s.DB.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", n)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// ---- key/value ----

// Get reads a setting kept in the database; ErrNotFound when unset.
func (s *Store) Get(ctx context.Context, key string) (string, error) {
	var v string
	err := s.DB.QueryRowContext(ctx, "SELECT value FROM kv WHERE key = ?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return v, err
}

// Set keeps a setting in the database.
func (s *Store) Set(ctx context.Context, key, value string) error {
	_, err := s.DB.ExecContext(ctx, "INSERT INTO kv(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", key, value)
	return err
}

// LoadJSON reads a setting kept as JSON, decoded over def; def when unset. One
// that doesn't decode comes back as far as it got, with the error.
func LoadJSON[T any](ctx context.Context, s *Store, key string, def T) (T, error) {
	v, err := s.Get(ctx, key)
	if errors.Is(err, ErrNotFound) {
		return def, nil
	}
	if err != nil {
		return def, err
	}
	err = json.Unmarshal([]byte(v), &def)
	return def, err
}

// SaveJSON keeps a setting in the database as JSON.
func SaveJSON(ctx context.Context, s *Store, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.Set(ctx, key, string(b))
}

// ---- server builds ----

// ServerBuilds maps the SHA-256 of each server program the manager has
// learned to the version it shipped as.
func (s *Store) ServerBuilds(ctx context.Context) (map[string]string, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT sha256, version FROM server_builds")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var sum, v string
		if err := rows.Scan(&sum, &v); err != nil {
			return nil, err
		}
		out[sum] = v
	}
	return out, rows.Err()
}

// AddServerBuild records the version a server program with this SHA-256
// shipped as.
func (s *Store) AddServerBuild(ctx context.Context, sha256, version string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO server_builds(sha256, version, seen) VALUES(?, ?, ?)
		ON CONFLICT(sha256) DO UPDATE SET version = excluded.version, seen = excluded.seen`, sha256, version, time.Now().Unix())
	return err
}

// ---- instances ----

// Instances lists every server's settings.
func (s *Store) Instances(ctx context.Context) ([]instance.Def, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT id, name, dir, auto_start, auto_restart, auto_update, shared_mods, restart_times, restart_hours FROM instances ORDER BY created_at")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []instance.Def
	for rows.Next() {
		var d instance.Def
		var times string
		if err := rows.Scan(&d.ID, &d.Name, &d.Dir, &d.AutoStart, &d.AutoRestart, &d.AutoUpdate, &d.SharedMods, &times, &d.RestartHours); err != nil {
			return nil, err
		}
		d.RestartTimes = []string{}
		if times != "" {
			d.RestartTimes = strings.Split(times, ",")
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// CreateInstance adds a server.
func (s *Store) CreateInstance(ctx context.Context, d instance.Def) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO instances(id, name, dir, auto_start, auto_restart, auto_update, shared_mods, restart_times, restart_hours, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.Name, d.Dir, d.AutoStart, d.AutoRestart, d.AutoUpdate, d.SharedMods, strings.Join(d.RestartTimes, ","), d.RestartHours, time.Now().Unix())
	return err
}

// UpdateInstance saves a server's settings; ErrNotFound when it is gone.
func (s *Store) UpdateInstance(ctx context.Context, d instance.Def) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE instances SET name = ?, auto_start = ?, auto_restart = ?, auto_update = ?, shared_mods = ?,
		restart_times = ?, restart_hours = ? WHERE id = ?`,
		d.Name, d.AutoStart, d.AutoRestart, d.AutoUpdate, d.SharedMods, strings.Join(d.RestartTimes, ","), d.RestartHours, d.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// MoveInstance changes where an instance's folder is.
func (s *Store) MoveInstance(ctx context.Context, id, dir string) error {
	res, err := s.DB.ExecContext(ctx, "UPDATE instances SET dir = ? WHERE id = ?", dir, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteInstance removes an instance and everything the panel keeps about it:
// its admins, announcements, samples, player history, visits, throwdowns and
// board scores, its park pools, and the roles granted on it. All of it goes, so a server added later under
// the same ID starts with none of it, and none of its players.
func (s *Store) DeleteInstance(ctx context.Context, id string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		"DELETE FROM managed_admins WHERE instance_id = ?",
		"DELETE FROM announcements WHERE instance_id = ?",
		"DELETE FROM perf_samples WHERE instance_id = ?",
		"DELETE FROM net_samples WHERE instance_id = ?",
		"DELETE FROM player_history WHERE instance_id = ?",
		"DELETE FROM player_sessions WHERE instance_id = ?",
		"DELETE FROM throwdowns WHERE instance_id = ?", // and their players with them
		"DELETE FROM board_scores WHERE instance_id = ?",
		"DELETE FROM user_roles WHERE instance_id = ?",
		"DELETE FROM kv WHERE key = 'parks:' || ?", // its park pools (internal/parks)
		"DELETE FROM instances WHERE id = ?",
	} {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ManagedAdmins lists the in-game admins the manager added to an instance for
// panel users, as opposed to admins added by hand.
func (s *Store) ManagedAdmins(ctx context.Context, instanceID string) ([]string, error) {
	return queryAll(ctx, s.DB, func(id *string) []any { return []any{id} },
		"SELECT steam_id FROM managed_admins WHERE instance_id = ? ORDER BY steam_id", instanceID)
}

// SetManagedAdmins replaces the in-game admins the manager added to an instance.
func (s *Store) SetManagedAdmins(ctx context.Context, instanceID string, ids []string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM managed_admins WHERE instance_id = ?", instanceID); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, "INSERT INTO managed_admins(instance_id, steam_id) VALUES(?, ?)", instanceID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ---- announcements ----

// AllInstances scopes an announcement to every server.
const AllInstances = "*"

// Announcement is a chat message sent on a timer, to one server or, with
// AllInstances, to all.
type Announcement struct {
	ID       int64  `json:"id"`
	Instance string `json:"instance"` // AllInstances for every server
	Message  string `json:"message"`
	Interval int    `json:"interval"` // seconds
	Enabled  bool   `json:"enabled"`
	ChatOnly bool   `json:"chatOnly,omitempty"` // without ReSkate 2.0.2's card
}

const announcementCols = "id, instance_id, message, interval, enabled, chat_only"

// fields are where announcementCols scan to.
func (a *Announcement) fields() []any {
	return []any{&a.ID, &a.Instance, &a.Message, &a.Interval, &a.Enabled, &a.ChatOnly}
}

// Announcements lists one server's announcements and those for every server,
// or all of them when instanceID is "".
func (s *Store) Announcements(ctx context.Context, instanceID string) ([]Announcement, error) {
	q := "SELECT " + announcementCols + " FROM announcements"
	var args []any
	if instanceID != "" {
		q += " WHERE instance_id IN (?, ?)"
		args = append(args, instanceID, AllInstances)
	}
	return queryAll(ctx, s.DB, (*Announcement).fields, q+" ORDER BY id", args...)
}

// Announcement finds an announcement by ID.
func (s *Store) Announcement(ctx context.Context, id int64) (Announcement, error) {
	var a Announcement
	err := s.DB.QueryRowContext(ctx, "SELECT "+announcementCols+" FROM announcements WHERE id = ?", id).Scan(a.fields()...)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

// SaveAnnouncement inserts a (when a.ID is 0, setting it) or updates it.
func (s *Store) SaveAnnouncement(ctx context.Context, a *Announcement) error {
	if a.ID == 0 {
		res, err := s.DB.ExecContext(ctx, "INSERT INTO announcements(instance_id, message, interval, enabled, chat_only, created_at) VALUES(?, ?, ?, ?, ?, ?)",
			a.Instance, a.Message, a.Interval, a.Enabled, a.ChatOnly, time.Now().Unix())
		if err != nil {
			return err
		}
		a.ID, err = res.LastInsertId()
		return err
	}
	res, err := s.DB.ExecContext(ctx, "UPDATE announcements SET instance_id = ?, message = ?, interval = ?, enabled = ?, chat_only = ? WHERE id = ?",
		a.Instance, a.Message, a.Interval, a.Enabled, a.ChatOnly, a.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteAnnouncement removes an announcement.
func (s *Store) DeleteAnnouncement(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM announcements WHERE id = ?", id)
	return err
}

// ---- player history ----

// SeenPlayer records a player joining an instance at joined (unix seconds),
// under the name they used: one more visit, open until LeftPlayer closes it.
func (s *Store) SeenPlayer(ctx context.Context, instanceID, steamID, name string, joined int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO player_history(instance_id, steam_id, name, first_seen, last_seen, sessions)
		VALUES(?, ?, ?, ?, ?, 1)
		ON CONFLICT(instance_id, steam_id) DO UPDATE SET name = excluded.name, last_seen = MAX(last_seen, excluded.last_seen), sessions = sessions + 1`,
		instanceID, steamID, name, joined, joined); err != nil {
		return err
	}
	// Ignored when the leave was written first: the two come in either order.
	if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO player_sessions(instance_id, steam_id, joined_at) VALUES(?, ?, ?)",
		instanceID, steamID, joined); err != nil {
		return err
	}
	return tx.Commit()
}

// leaveReasonMax caps what a visit keeps of why it ended, which is the
// server's text.
const leaveReasonMax = 256

// LeftPlayer closes a player's visit to an instance, the one that began at
// joined, at left (unix seconds) and with why it ended. Written before the
// join, it makes the visit closed already.
func (s *Store) LeftPlayer(ctx context.Context, instanceID, steamID string, joined, left int64, reason string) error {
	left = max(left, joined)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO player_sessions(instance_id, steam_id, joined_at, left_at, reason) VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(instance_id, steam_id, joined_at) DO UPDATE SET left_at = excluded.left_at, reason = excluded.reason`,
		instanceID, steamID, joined, left, clip(reason, leaveReasonMax)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE player_history SET last_seen = MAX(last_seen, ?) WHERE instance_id = ? AND steam_id = ?",
		left, instanceID, steamID); err != nil {
		return err
	}
	return tx.Commit()
}

// CloseOpenSessions closes the visits still open, which only a manager that
// ended without closing them leaves: each as if the player left at the last
// sample taken of their server since they joined (one every 30 s), or as
// they joined when there is none. It runs before any server starts, and
// reports how many it closed.
func (s *Store) CloseOpenSessions(ctx context.Context, reason string) (int, error) {
	type open struct {
		instance, steam string
		joined, last    int64
	}
	list, err := queryAll(ctx, s.DB, func(o *open) []any { return []any{&o.instance, &o.steam, &o.joined, &o.last} },
		`SELECT s.instance_id, s.steam_id, s.joined_at,
			COALESCE((SELECT MAX(p.at) FROM perf_samples p WHERE p.instance_id = s.instance_id AND p.at >= s.joined_at), s.joined_at)
		FROM player_sessions s WHERE s.left_at IS NULL`)
	if err != nil {
		return 0, err
	}
	for _, o := range list {
		if err := s.LeftPlayer(ctx, o.instance, o.steam, o.joined, o.last, reason); err != nil {
			return 0, err
		}
	}
	return len(list), nil
}

// PlayerRecord is one player in an instance's history.
type PlayerRecord struct {
	SteamID   string `json:"id"`
	Name      string `json:"name"`
	FirstSeen int64  `json:"firstSeen"`
	LastSeen  int64  `json:"lastSeen"`
	Sessions  int    `json:"sessions"`
	// Playtime is seconds on the server, a visit going on now included; nil
	// for a player not seen there since the manager began keeping visits.
	Playtime *int64 `json:"playtime"`
	// PlaytimeAll is the same on all the servers PlayerHistory was given;
	// nil when it was given none.
	PlaytimeAll *int64 `json:"playtimeAll,omitempty"`
	// Throwdowns is how many finished throwdowns they played on the server,
	// not counting those they quit, and Wins how many of those they won.
	Throwdowns int `json:"throwdowns"`
	Wins       int `json:"wins"`
	// ThrowdownsAll and WinsAll are the same on all the servers PlayerHistory
	// was given; nil when it was given none.
	ThrowdownsAll *int `json:"throwdownsAll,omitempty"`
	WinsAll       *int `json:"winsAll,omitempty"`
}

// PrunePlayers forgets the players last seen before `before`, their visits
// and their places in throwdowns, on every server, and reports how many
// players went. A throwdown goes once nobody in it is left to count it. A
// board score goes when nobody seen on any server has it, unless it changed
// since; a past month's or week's goes once it began before `before`.
func (s *Store) PrunePlayers(ctx context.Context, before int64) (int64, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM player_sessions WHERE left_at < ?1 AND NOT EXISTS (SELECT 1 FROM player_history h
			WHERE h.instance_id = player_sessions.instance_id AND h.steam_id = player_sessions.steam_id AND h.last_seen >= ?1)`,
		`DELETE FROM throwdown_players WHERE steam_id != '' AND throwdown_id IN (SELECT id FROM throwdowns WHERE finished_at < ?1)
			AND NOT EXISTS (SELECT 1 FROM throwdowns t JOIN player_history h ON h.instance_id = t.instance_id
				WHERE t.id = throwdown_players.throwdown_id AND h.steam_id = throwdown_players.steam_id AND h.last_seen >= ?1)`,
		`DELETE FROM throwdowns WHERE finished_at < ?1 AND NOT EXISTS (SELECT 1 FROM throwdown_players p
			WHERE p.throwdown_id = throwdowns.id AND p.steam_id != '')`,
	} {
		if _, err := tx.ExecContext(ctx, q, before); err != nil {
			return 0, err
		}
	}
	res, err := tx.ExecContext(ctx, "DELETE FROM player_history WHERE last_seen < ?", before)
	if err != nil {
		return 0, err
	}
	now := time.Now()
	if _, err := tx.ExecContext(ctx, `DELETE FROM board_scores WHERE (at < ?1 AND steam_id NOT IN (SELECT steam_id FROM player_history))
		OR (period = 'month' AND starts < MIN(?1, ?2)) OR (period = 'week' AND starts < MIN(?1, ?3))`,
		before, PeriodStart("month", now), PeriodStart("week", now)); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// A player's playtime as of a time, their finished throwdowns and the ones
// they won, each on the servers a condition on s or t picks, which follows.
const (
	playtimeOf   = "(SELECT SUM(MAX(0, COALESCE(s.left_at, ?) - s.joined_at)) FROM player_sessions s WHERE s.steam_id = h.steam_id AND s.instance_id "
	throwdownsOf = "(SELECT COUNT(*) FROM throwdown_players p JOIN throwdowns t ON t.id = p.throwdown_id WHERE p.quit = 0 AND p.steam_id = h.steam_id AND t.instance_id "
	winsOf       = "(SELECT COUNT(*) FROM throwdown_players p JOIN throwdowns t ON t.id = p.throwdown_id WHERE p.won = 1 AND p.steam_id = h.steam_id AND t.instance_id "
)

// PlayerHistory lists up to limit players an instance has seen, last seen
// first, with their playtime and throwdowns there. Given a search, only
// those whose SteamID is it or whose name has it. Given network, the servers
// each player's PlaytimeAll, ThrowdownsAll and WinsAll add up.
func (s *Store) PlayerHistory(ctx context.Context, instanceID, search string, limit int, network ...string) ([]PlayerRecord, error) {
	now := time.Now().Unix()
	q := "SELECT h.steam_id, h.name, h.first_seen, h.last_seen, h.sessions, " +
		playtimeOf + "= h.instance_id), " + throwdownsOf + "= h.instance_id), " + winsOf + "= h.instance_id), "
	args := []any{now}
	if len(network) > 0 {
		in := "IN (?" + strings.Repeat(", ?", len(network)-1) + "))"
		q += playtimeOf + in + ", " + throwdownsOf + in + ", " + winsOf + in
		args = append(args, now)
		for range 3 {
			for _, id := range network {
				args = append(args, id)
			}
		}
	} else {
		q += "NULL, NULL, NULL"
	}
	q += " FROM player_history h WHERE h.instance_id = ?"
	args = append(args, instanceID)
	if search != "" {
		q += " AND (h.steam_id = ? OR h.name LIKE ? ESCAPE '\\')"
		esc := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(search)
		args = append(args, search, "%"+esc+"%")
	}
	q += " ORDER BY h.last_seen DESC LIMIT ?"
	args = append(args, limit)
	return queryAll(ctx, s.DB, func(p *PlayerRecord) []any {
		return []any{&p.SteamID, &p.Name, &p.FirstSeen, &p.LastSeen, &p.Sessions, &p.Playtime, &p.Throwdowns, &p.Wins,
			&p.PlaytimeAll, &p.ThrowdownsAll, &p.WinsAll}
	}, q, args...)
}

// ---- throwdowns ----

// throwdownNameMax caps a name kept with a throwdown, which a player picks.
const throwdownNameMax = 128

// AddThrowdown keeps a finished throwdown's results, as finished at `at`
// (unix seconds). Quitting counts for nothing. Of a throwdown two or more
// played, those who stayed with the most points, above 0, win: each of them
// on a tie, and the one left when everyone else quit. S.K.A.T.E. is won on
// letters, which the server never sees, so only that one wins it, with a
// landed try.
func (s *Store) AddThrowdown(ctx context.Context, instanceID string, at int64, t logparse.Throwdown) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, "INSERT INTO throwdowns(instance_id, mode, leader_id, leader, finished_at) VALUES(?, ?, ?, ?, ?)",
		instanceID, t.Mode, t.Leader.ID, clip(t.Leader.Name, throwdownNameMax), at)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	stayed, top := 0, int64(0)
	for _, r := range t.Results {
		if !r.Quit {
			stayed++
			top = max(top, r.Points)
		}
	}
	for i, r := range t.Results {
		var place, points, landed, missed any
		if t.Mode == logparse.ModeSkate {
			landed, missed = r.Landed, r.Missed
		} else {
			place, points = r.Place, r.Points
		}
		var won bool
		switch {
		case len(t.Results) < 2 || r.Quit:
		case t.Mode == logparse.ModeSkate:
			won = stayed == 1 && r.Landed > 0
		default:
			won = top > 0 && r.Points == top
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO throwdown_players(throwdown_id, seat, steam_id, name, place, points, landed, missed, quit, won)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, i+1, r.ID, clip(r.Name, throwdownNameMax), place, points, landed, missed, r.Quit, won); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Playtime is a player's seconds on a server, or on all of them for "", a
// visit going on now counted up to now (unix seconds).
func (s *Store) Playtime(ctx context.Context, steamID, server string, now int64) (int64, error) {
	q := "SELECT COALESCE(SUM(MAX(0, COALESCE(left_at, ?) - joined_at)), 0) FROM player_sessions WHERE steam_id = ?"
	args := []any{now, steamID}
	if server != "" {
		q += " AND instance_id = ?"
		args = append(args, server)
	}
	var n int64
	err := s.Read.QueryRowContext(ctx, q, args...).Scan(&n)
	return n, err
}

// Visit is one of a player's visits to a server; LeftAt is nil while it goes
// on.
type Visit struct {
	Server   string
	JoinedAt int64
	LeftAt   *int64
	Reason   string
}

// Visits is up to limit of a player's latest visits, on any server, newest
// first.
func (s *Store) Visits(ctx context.Context, steamID string, limit int) ([]Visit, error) {
	return queryAll(ctx, s.Read, func(v *Visit) []any { return []any{&v.Server, &v.JoinedAt, &v.LeftAt, &v.Reason} },
		"SELECT instance_id, joined_at, left_at, reason FROM player_sessions WHERE steam_id = ? ORDER BY joined_at DESC LIMIT ?", steamID, limit)
}

// ---- plugin stores ----

// PluginStore is all a plugin keeps in its store, each value as package
// luavm encodes it.
func (s *Store) PluginStore(ctx context.Context, plugin string) (map[string][]byte, error) {
	type pair struct {
		k string
		v []byte
	}
	list, err := queryAll(ctx, s.DB, func(p *pair) []any { return []any{&p.k, &p.v} }, "SELECT key, value FROM plugin_store WHERE plugin = ?", plugin)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]byte, len(list))
	for _, p := range list {
		out[p.k] = p.v
	}
	return out, nil
}

// SavePluginStore writes the keys of a plugin's store that changed, a nil
// value deleting its key.
func (s *Store) SavePluginStore(ctx context.Context, plugin string, changed map[string][]byte) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for k, v := range changed {
		if v == nil {
			_, err = tx.ExecContext(ctx, "DELETE FROM plugin_store WHERE plugin = ? AND key = ?", plugin, k)
		} else {
			_, err = tx.ExecContext(ctx, "INSERT INTO plugin_store(plugin, key, value) VALUES(?, ?, ?) ON CONFLICT DO UPDATE SET value = excluded.value", plugin, k, v)
		}
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// MovePluginStore moves every key of plugin from's store into to's, each
// with prefix before it, and from's boards too, and is how many keys moved.
// If to has one of them already, nothing moves.
func (s *Store) MovePluginStore(ctx context.Context, from, to, prefix string) (int, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var clash string
	err = tx.QueryRowContext(ctx, "SELECT b.key FROM plugin_store a JOIN plugin_store b ON b.plugin = ? AND b.key = ? || a.key WHERE a.plugin = ? LIMIT 1", to, prefix, from).Scan(&clash)
	switch {
	case err == nil:
		return 0, fmt.Errorf("this one's store has %s already", clash)
	case !errors.Is(err, sql.ErrNoRows):
		return 0, err
	}
	res, err := tx.ExecContext(ctx, "UPDATE plugin_store SET plugin = ?, key = ? || key WHERE plugin = ?", to, prefix, from)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if _, err := tx.ExecContext(ctx, "UPDATE boards SET plugin = ? WHERE plugin = ?", to, from); err != nil {
		return 0, err
	}
	return int(n), tx.Commit()
}

// ---- boards ----

// How a board keeps a player's score: adding up what they're given, or the
// highest or the lowest of it.
const (
	KeepSum     = "sum"
	KeepBest    = "best"
	KeepFastest = "fastest"
)

// PlaytimeBoard is the board every player is on: their time on the servers,
// from their visits, in seconds.
const PlaytimeBoard = "playtime"

// Periods are what a board ranks over: all time, the month and the week.
var Periods = []string{"all", "month", "week"}

// PeriodStart is when the period holding t began, in unix seconds: 0 for all
// time, else midnight on the 1st of its month or the Monday of its week, on
// the manager's clock (TZ), as scheduled restarts are.
func PeriodStart(period string, t time.Time) int64 {
	t = t.In(time.Local)
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
	switch period {
	case "month":
		return day.AddDate(0, 0, 1-t.Day()).Unix()
	case "week":
		return day.AddDate(0, 0, -(int(t.Weekday())+6)%7).Unix()
	}
	return 0
}

// Board is a board a plugin defined.
type Board struct {
	ID     string
	Plugin string
	Title  string
	Unit   string
	Keep   string
	Public bool
}

// Boards is every board plugins defined.
func (s *Store) Boards(ctx context.Context) ([]Board, error) {
	return queryAll(ctx, s.DB, func(b *Board) []any { return []any{&b.ID, &b.Plugin, &b.Title, &b.Unit, &b.Keep, &b.Public} },
		"SELECT id, plugin, title, unit, keep, public FROM boards ORDER BY id")
}

// SaveBoards keeps boards as defined, new or changed.
func (s *Store) SaveBoards(ctx context.Context, list []Board) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, b := range list {
		if _, err := tx.ExecContext(ctx, `INSERT INTO boards(id, plugin, title, unit, keep, public) VALUES(?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET plugin = excluded.plugin, title = excluded.title, unit = excluded.unit, keep = excluded.keep, public = excluded.public`,
			b.ID, b.Plugin, b.Title, b.Unit, b.Keep, b.Public); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Score is what a player was given on a board, on a server, at a time (unix
// seconds).
type Score struct {
	Board   string
	Keep    string // the board's
	Server  string
	SteamID string
	Value   float64
	At      int64
}

// AddScores adds scores to their boards: each to the player's rows on its
// server for all time, and for the month and the week it falls in.
func (s *Store) AddScores(ctx context.Context, list []Score) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO board_scores(board, period, starts, instance_id, steam_id, value, at) VALUES(?1, ?2, ?3, ?4, ?5, ?6, ?7)
		ON CONFLICT DO UPDATE SET
			value = CASE ?8 WHEN 'sum' THEN value + excluded.value WHEN 'best' THEN MAX(value, excluded.value) ELSE MIN(value, excluded.value) END,
			at = CASE WHEN ?8 = 'sum' OR (?8 = 'best' AND excluded.value > value) OR (?8 = 'fastest' AND excluded.value < value) THEN excluded.at ELSE at END`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, sc := range list {
		for _, p := range Periods {
			if _, err := stmt.ExecContext(ctx, sc.Board, p, PeriodStart(p, time.Unix(sc.At, 0)), sc.Server, sc.SteamID, sc.Value, sc.At, sc.Keep); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// BoardQuery picks a board's scores over the period holding Now: on one
// server, or with none on all of them together, a sum adding the servers' up,
// a best taking the highest and a fastest the lowest.
type BoardQuery struct {
	Board  Board
	Server string
	Period string
	Now    time.Time
}

// Ranked is a player's place on a board.
type Ranked struct {
	Rank    int
	SteamID string
	Name    string // the latest they had on any server; "" when none is known
	Value   any    // int64, or float64 for a fraction
}

// Ranking is everyone on a board, best first. Equal scores share a rank,
// whoever got there first listed first. It reads on Read, so however long a
// big board takes, it holds up no write.
func (s *Store) Ranking(ctx context.Context, q BoardQuery) ([]Ranked, error) {
	start := PeriodStart(q.Period, q.Now)
	var from string
	var args []any
	if q.Board.ID == PlaytimeBoard { // each visit's part in the period
		now := q.Now.Unix()
		from = `SELECT steam_id, SUM(MAX(0, COALESCE(left_at, ?) - MAX(joined_at, ?))) AS v, MAX(COALESCE(left_at, ?)) AS at
			FROM player_sessions WHERE (left_at IS NULL OR left_at > ?)`
		args = []any{now, start, now, start}
	} else {
		// A lone MAX or MIN takes at from the row it picked.
		agg := map[string]string{KeepSum: "SUM(value) AS v, MAX(at) AS at", KeepBest: "MAX(value) AS v, at", KeepFastest: "MIN(value) AS v, at"}[q.Board.Keep]
		from = "SELECT steam_id, " + agg + " FROM board_scores WHERE board = ? AND period = ? AND starts = ?"
		args = []any{q.Board.ID, q.Period, start}
	}
	if q.Server != "" {
		from += " AND instance_id = ?"
		args = append(args, q.Server)
	}
	from += " GROUP BY steam_id"
	if q.Board.ID == PlaytimeBoard {
		from += " HAVING v > 0"
	}
	order := "s.v DESC"
	if q.Board.Keep == KeepFastest {
		order = "s.v"
	}
	list, err := queryAll(ctx, s.Read, func(r *Ranked) []any { return []any{&r.SteamID, &r.Name, &r.Value} },
		`SELECT s.steam_id, COALESCE((SELECT h.name FROM player_history h WHERE h.steam_id = s.steam_id ORDER BY h.last_seen DESC LIMIT 1), ''), s.v
		FROM (`+from+`) s ORDER BY `+order+`, s.at, s.steam_id`, args...)
	for i := range list {
		if list[i].Rank = i + 1; i > 0 && number(list[i].Value) == number(list[i-1].Value) {
			list[i].Rank = list[i-1].Rank
		}
	}
	return list, err
}

// number is a value SQLite gave as an integer or a real, to compare.
func number(v any) float64 {
	switch v := v.(type) {
	case int64:
		return float64(v)
	case float64:
		return v
	}
	return 0
}

// ---- audit log ----

// AuditEntry is one thing someone did, for the audit log.
type AuditEntry struct {
	ID       int64  `json:"id"`
	At       int64  `json:"at"`
	UserID   int64  `json:"userId,omitempty"`
	Username string `json:"username,omitempty"`
	Instance string `json:"instance,omitempty"`
	Action   string `json:"action"`
	Detail   string `json:"detail,omitempty"`
	IP       string `json:"ip,omitempty"`
}

// auditDetailMax caps what one audit entry keeps of its detail, which can
// carry text someone typed, such as a console command.
const auditDetailMax = 1024

// Audit writes an entry to the audit log.
func (s *Store) Audit(ctx context.Context, e AuditEntry) error {
	if e.At == 0 {
		e.At = time.Now().Unix()
	}
	e.Detail = clip(e.Detail, auditDetailMax)
	_, err := s.DB.ExecContext(ctx, "INSERT INTO audit_log(at, user_id, username, instance_id, action, detail, ip) VALUES(?, ?, ?, ?, ?, ?, ?)",
		e.At, e.UserID, e.Username, e.Instance, e.Action, e.Detail, e.IP)
	return err
}

// PruneAudit drops the entries written before `before` and reports how many
// went.
func (s *Store) PruneAudit(ctx context.Context, before int64) (int64, error) {
	res, err := s.DB.ExecContext(ctx, "DELETE FROM audit_log WHERE at < ?", before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// AuditLog returns entries newest first, older than `before` (0: from now).
func (s *Store) AuditLog(ctx context.Context, instanceID string, before int64, limit int) ([]AuditEntry, error) {
	q := "SELECT id, at, COALESCE(user_id, 0), COALESCE(username, ''), COALESCE(instance_id, ''), action, COALESCE(detail, ''), COALESCE(ip, '') FROM audit_log WHERE 1=1"
	var args []any
	if instanceID != "" {
		q += " AND instance_id = ?"
		args = append(args, instanceID)
	}
	if before > 0 {
		q += " AND id < ?"
		args = append(args, before)
	}
	q += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)
	return queryAll(ctx, s.DB, func(e *AuditEntry) []any {
		return []any{&e.ID, &e.At, &e.UserID, &e.Username, &e.Instance, &e.Action, &e.Detail, &e.IP}
	}, q, args...)
}

// ---- performance ----

// PerfSample is one reading of a running server's process.
type PerfSample struct {
	At      int64   `json:"at"`  // unix seconds
	Run     int64   `json:"run"` // the process's start, unix seconds
	CPU     float64 `json:"cpu"` // percent of the whole machine
	Mem     int64   `json:"mem"` // bytes
	Players int     `json:"players"`
}

// AddPerfSample records a sample of a running server.
func (s *Store) AddPerfSample(ctx context.Context, instanceID string, p PerfSample) error {
	_, err := s.DB.ExecContext(ctx, "INSERT OR REPLACE INTO perf_samples(instance_id, at, run, cpu, mem, players) VALUES(?, ?, ?, ?, ?, ?)",
		instanceID, p.At, p.Run, p.CPU, p.Mem, p.Players)
	return err
}

// LatestPerf returns an instance's newest sample, or nil when it has none.
func (s *Store) LatestPerf(ctx context.Context, instanceID string) (*PerfSample, error) {
	var p PerfSample
	err := s.DB.QueryRowContext(ctx, "SELECT at, run, cpu, mem, players FROM perf_samples WHERE instance_id = ? ORDER BY at DESC LIMIT 1", instanceID).
		Scan(&p.At, &p.Run, &p.CPU, &p.Mem, &p.Players)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// PrunePerf drops samples taken before `before`.
func (s *Store) PrunePerf(ctx context.Context, before int64) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM perf_samples WHERE at < ?", before)
	return err
}

// AddNetSample records a [network] line of an instance's run, logged at at
// (unix seconds). What the line lacked is stored as NULL.
func (s *Store) AddNetSample(ctx context.Context, instanceID string, at, run int64, n logparse.Network) error {
	var queued, queueMs, failed, skipped, dropped, busy, pass, gap any
	if n.Full {
		queued, queueMs, failed, skipped, dropped = n.QueuedKB, n.QueueMs, n.Failed, n.Skipped, n.Dropped
	}
	if n.Loop {
		busy, pass, gap = n.BusyPct, n.PassMs, n.GapMs
	}
	_, err := s.DB.ExecContext(ctx, `INSERT OR REPLACE INTO net_samples(instance_id, at, run, players, out_kbs, in_kbs, ping_ms,
		queued_kb, queue_ms, failed, skipped, dropped, busy_pct, pass_ms, gap_ms) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		instanceID, at, run, n.Players, n.OutKBs, n.InKBs, n.PingMs, queued, queueMs, failed, skipped, dropped, busy, pass, gap)
	return err
}

// PruneNet drops network samples taken before `before`.
func (s *Store) PruneNet(ctx context.Context, before int64) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM net_samples WHERE at < ?", before)
	return err
}

// NetPoint sums up an instance's network samples within one time bucket.
// Fields are nil when no sample in it had them, from servers before 1.1.5.
type NetPoint struct {
	At      int64    `json:"at"`      // bucket start
	Players int      `json:"players"` // most at once
	Out     float64  `json:"out"`     // KB/s, averaged
	OutMax  int      `json:"outMax"`
	In      float64  `json:"in"`
	InMax   int      `json:"inMax"`
	Ping    int      `json:"ping"`    // the worst
	Queued  *int     `json:"queued"`  // KB, the most
	QueueMs *int     `json:"queueMs"` // the longest wait
	Failed  *int64   `json:"failed"`  // sends that failed in the bucket
	Skipped *int64   `json:"skipped"`
	Dropped *int64   `json:"dropped"`
	Busy    *int     `json:"busy"`   // percent of the main loop, the most
	PassMs  *float64 `json:"passMs"` // the loop's longest pass
	GapMs   *float64 `json:"gapMs"`  // and its longest gap
}

// NetPoints returns an instance's network samples since `since`, summed up
// into buckets `bucket` seconds wide. A run's totals become how many in each
// bucket; the first sample in range has nothing to count from, so adds none.
func (s *Store) NetPoints(ctx context.Context, instanceID string, since, bucket int64) ([]NetPoint, error) {
	return queryAll(ctx, s.DB, func(p *NetPoint) []any {
		return []any{&p.At, &p.Players, &p.Out, &p.OutMax, &p.In, &p.InMax, &p.Ping,
			&p.Queued, &p.QueueMs, &p.Failed, &p.Skipped, &p.Dropped, &p.Busy, &p.PassMs, &p.GapMs}
	}, `SELECT at - at % ? AS t, MAX(players), AVG(out_kbs), MAX(out_kbs), AVG(in_kbs), MAX(in_kbs), MAX(ping_ms),
		MAX(queued_kb), MAX(queue_ms), SUM(failed_n), SUM(skipped_n), SUM(dropped_n), MAX(busy_pct), MAX(pass_ms), MAX(gap_ms)
		FROM (SELECT *, failed - LAG(failed) OVER run_order AS failed_n, skipped - LAG(skipped) OVER run_order AS skipped_n,
			dropped - LAG(dropped) OVER run_order AS dropped_n
			FROM net_samples WHERE instance_id = ? AND at >= ? WINDOW run_order AS (PARTITION BY run ORDER BY at))
		GROUP BY t ORDER BY t`, bucket, instanceID, since)
}

// PerfPoint sums up the samples of one run within one time bucket.
type PerfPoint struct {
	At      int64   `json:"at"` // bucket start
	Run     int64   `json:"run"`
	CPU     float64 `json:"cpu"` // average
	CPUMax  float64 `json:"cpuMax"`
	Mem     int64   `json:"mem"`     // average
	Players int     `json:"players"` // most at once
}

// PerfPoints returns samples since `since`, averaged into buckets `bucket` seconds wide.
func (s *Store) PerfPoints(ctx context.Context, instanceID string, since, bucket int64) ([]PerfPoint, error) {
	return queryAll(ctx, s.DB, func(p *PerfPoint) []any {
		return []any{&p.At, &p.Run, &p.CPU, &p.CPUMax, &p.Mem, &p.Players}
	}, `SELECT at - at % ? AS t, run, AVG(cpu), MAX(cpu), CAST(AVG(mem) AS INTEGER), MAX(players)
		FROM perf_samples WHERE instance_id = ? AND at >= ? GROUP BY t, run ORDER BY t, run`, bucket, instanceID, since)
}

// PerfRun sums up one run's samples.
type PerfRun struct {
	Run     int64   `json:"run"`
	First   int64   `json:"first"` // first and last sample
	Last    int64   `json:"last"`
	CPU     float64 `json:"cpu"` // average
	CPUMax  float64 `json:"cpuMax"`
	Mem     int64   `json:"mem"` // average
	MemMax  int64   `json:"memMax"`
	Players int     `json:"players"` // most at once
}

// PerfRuns lists the runs with samples since `since`, newest first.
func (s *Store) PerfRuns(ctx context.Context, instanceID string, since int64, limit int) ([]PerfRun, error) {
	return queryAll(ctx, s.DB, func(r *PerfRun) []any {
		return []any{&r.Run, &r.First, &r.Last, &r.CPU, &r.CPUMax, &r.Mem, &r.MemMax, &r.Players}
	}, `SELECT run, MIN(at), MAX(at), AVG(cpu), MAX(cpu), CAST(AVG(mem) AS INTEGER), MAX(mem), MAX(players)
		FROM perf_samples WHERE instance_id = ? AND at >= ? GROUP BY run ORDER BY run DESC LIMIT ?`, instanceID, since, limit)
}

// ServerPerfPoint sums up one server's samples within one time bucket, all
// its runs together.
type ServerPerfPoint struct {
	Instance string
	At       int64 // bucket start
	CPU      float64
	CPUMax   float64
	Mem      int64
	MemMax   int64
	Samples  int // CPU and Mem are sums over this many samples
}

// ServersPerfPoints returns every server's samples since `since`, summed into
// buckets `bucket` seconds wide, by server and then time.
func (s *Store) ServersPerfPoints(ctx context.Context, since, bucket int64) ([]ServerPerfPoint, error) {
	return queryAll(ctx, s.DB, func(p *ServerPerfPoint) []any {
		return []any{&p.Instance, &p.At, &p.CPU, &p.CPUMax, &p.Mem, &p.MemMax, &p.Samples}
	}, `SELECT instance_id, at - at % ? AS t, SUM(cpu), MAX(cpu), SUM(mem), MAX(mem), COUNT(*)
		FROM perf_samples WHERE at >= ? GROUP BY instance_id, t ORDER BY instance_id, t`, bucket, since)
}

// HostSample is one reading of the machine the manager runs on, with its
// servers summed up.
type HostSample struct {
	At         int64   `json:"at"`  // unix seconds
	CPU        float64 `json:"cpu"` // percent of the whole machine
	MemTotal   int64   `json:"memTotal"`
	MemUsed    int64   `json:"memUsed"`   // total minus available
	MemCached  int64   `json:"memCached"` // cache the system can hand back
	LimitMax   int64   `json:"limitMax"`  // the manager's cgroup memory limit; 0 without one
	LimitUsed  int64   `json:"limitUsed"`
	ServersCPU float64 `json:"serversCpu"`
	ServersMem int64   `json:"serversMem"`
	ManagerMem int64   `json:"managerMem"`
	Running    int     `json:"running"`
	Servers    int     `json:"servers"`
	Players    int     `json:"players"`
}

const hostColumns = "at, cpu, mem_total, mem_used, mem_cached, limit_max, limit_used, servers_cpu, servers_mem, manager_mem, running, servers, players"

// AddHostSample records a sample of the machine.
func (s *Store) AddHostSample(ctx context.Context, h HostSample) error {
	_, err := s.DB.ExecContext(ctx, "INSERT OR REPLACE INTO host_samples("+hostColumns+") VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		h.At, h.CPU, h.MemTotal, h.MemUsed, h.MemCached, h.LimitMax, h.LimitUsed, h.ServersCPU, h.ServersMem, h.ManagerMem, h.Running, h.Servers, h.Players)
	return err
}

// LatestHost returns the newest host sample, or nil when there is none.
func (s *Store) LatestHost(ctx context.Context) (*HostSample, error) {
	var h HostSample
	err := s.DB.QueryRowContext(ctx, "SELECT "+hostColumns+" FROM host_samples ORDER BY at DESC LIMIT 1").
		Scan(&h.At, &h.CPU, &h.MemTotal, &h.MemUsed, &h.MemCached, &h.LimitMax, &h.LimitUsed, &h.ServersCPU, &h.ServersMem, &h.ManagerMem, &h.Running, &h.Servers, &h.Players)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &h, nil
}

// PruneHost drops host samples taken before `before`.
func (s *Store) PruneHost(ctx context.Context, before int64) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM host_samples WHERE at < ?", before)
	return err
}

// HostPoint averages the host samples within one time bucket. Running,
// Servers and Players are the most at once.
type HostPoint struct {
	HostSample
	CPUMax  float64 `json:"cpuMax"`
	Samples int     `json:"-"`
}

// HostPoints returns host samples since `since`, averaged into buckets
// `bucket` seconds wide.
func (s *Store) HostPoints(ctx context.Context, since, bucket int64) ([]HostPoint, error) {
	return queryAll(ctx, s.DB, func(p *HostPoint) []any {
		return []any{&p.At, &p.CPU, &p.CPUMax, &p.MemTotal, &p.MemUsed, &p.MemCached, &p.LimitMax, &p.LimitUsed, &p.ServersCPU, &p.ServersMem,
			&p.ManagerMem, &p.Running, &p.Servers, &p.Players, &p.Samples}
	}, `SELECT at - at % ? AS t, AVG(cpu), MAX(cpu), CAST(AVG(mem_total) AS INTEGER), CAST(AVG(mem_used) AS INTEGER),
		CAST(AVG(mem_cached) AS INTEGER), MAX(limit_max), CAST(AVG(limit_used) AS INTEGER), AVG(servers_cpu), CAST(AVG(servers_mem) AS INTEGER),
		CAST(AVG(manager_mem) AS INTEGER), MAX(running), MAX(servers), MAX(players), COUNT(*)
		FROM host_samples WHERE at >= ? GROUP BY t ORDER BY t`, bucket, since)
}

// queryAll runs q and gathers a T from each row, scanned into the pointers
// fields gives; [] for no rows.
func queryAll[T any](ctx context.Context, db *sql.DB, fields func(*T) []any, q string, args ...any) ([]T, error) {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		var v T
		if err := rows.Scan(fields(&v)...); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// clip cuts s to at most n bytes on a character boundary, marking the cut,
// and makes it valid UTF-8.
func clip(s string, n int) string {
	s = strings.ToValidUTF8(s, string(utf8.RuneError))
	if len(s) <= n {
		return s
	}
	const mark = "…"
	cut := n - len(mark)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + mark
}
