// Package store is the manager's SQLite database.
package store

import (
	"context"
	"database/sql"
	"embed"
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

// Store is the manager's database.
type Store struct{ DB *sql.DB }

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
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.DB.Close() }

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
// its admins, announcements, samples, player history and the roles granted
// on it. All of it goes, so a server added later under the same ID starts
// with none of it, and none of its players.
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
		"DELETE FROM user_roles WHERE instance_id = ?",
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
	rows, err := s.DB.QueryContext(ctx, "SELECT steam_id FROM managed_admins WHERE instance_id = ? ORDER BY steam_id", instanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
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
}

// Announcements lists one server's announcements and those for every server,
// or all of them when instanceID is "".
func (s *Store) Announcements(ctx context.Context, instanceID string) ([]Announcement, error) {
	q := "SELECT id, instance_id, message, interval, enabled FROM announcements"
	var args []any
	if instanceID != "" {
		q += " WHERE instance_id IN (?, ?)"
		args = append(args, instanceID, AllInstances)
	}
	rows, err := s.DB.QueryContext(ctx, q+" ORDER BY id", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Announcement{}
	for rows.Next() {
		var a Announcement
		if err := rows.Scan(&a.ID, &a.Instance, &a.Message, &a.Interval, &a.Enabled); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Announcement finds an announcement by ID.
func (s *Store) Announcement(ctx context.Context, id int64) (Announcement, error) {
	var a Announcement
	err := s.DB.QueryRowContext(ctx, "SELECT id, instance_id, message, interval, enabled FROM announcements WHERE id = ?", id).
		Scan(&a.ID, &a.Instance, &a.Message, &a.Interval, &a.Enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

// SaveAnnouncement inserts a (when a.ID is 0, setting it) or updates it.
func (s *Store) SaveAnnouncement(ctx context.Context, a *Announcement) error {
	if a.ID == 0 {
		res, err := s.DB.ExecContext(ctx, "INSERT INTO announcements(instance_id, message, interval, enabled, created_at) VALUES(?, ?, ?, ?, ?)",
			a.Instance, a.Message, a.Interval, a.Enabled, time.Now().Unix())
		if err != nil {
			return err
		}
		a.ID, err = res.LastInsertId()
		return err
	}
	res, err := s.DB.ExecContext(ctx, "UPDATE announcements SET instance_id = ?, message = ?, interval = ?, enabled = ? WHERE id = ?",
		a.Instance, a.Message, a.Interval, a.Enabled, a.ID)
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

// SeenPlayer records a player joining an instance, under the name they used.
func (s *Store) SeenPlayer(ctx context.Context, instanceID, steamID, name string) error {
	now := time.Now().Unix()
	_, err := s.DB.ExecContext(ctx, `INSERT INTO player_history(instance_id, steam_id, name, first_seen, last_seen, sessions)
		VALUES(?, ?, ?, ?, ?, 1)
		ON CONFLICT(instance_id, steam_id) DO UPDATE SET name = excluded.name, last_seen = excluded.last_seen, sessions = sessions + 1`,
		instanceID, steamID, name, now, now)
	return err
}

// PlayerRecord is one player in an instance's history.
type PlayerRecord struct {
	SteamID   string `json:"id"`
	Name      string `json:"name"`
	FirstSeen int64  `json:"firstSeen"`
	LastSeen  int64  `json:"lastSeen"`
	Sessions  int    `json:"sessions"`
}

// PrunePlayers forgets the players last seen before `before`, on every
// server, and reports how many records went.
func (s *Store) PrunePlayers(ctx context.Context, before int64) (int64, error) {
	res, err := s.DB.ExecContext(ctx, "DELETE FROM player_history WHERE last_seen < ?", before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PlayerHistory lists up to limit players an instance has seen, last seen
// first. Given a search, only those whose SteamID is it or whose name has it.
func (s *Store) PlayerHistory(ctx context.Context, instanceID, search string, limit int) ([]PlayerRecord, error) {
	q := "SELECT steam_id, name, first_seen, last_seen, sessions FROM player_history WHERE instance_id = ?"
	args := []any{instanceID}
	if search != "" {
		q += " AND (steam_id = ? OR name LIKE ? ESCAPE '\\')"
		esc := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(search)
		args = append(args, search, "%"+esc+"%")
	}
	q += " ORDER BY last_seen DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PlayerRecord
	for rows.Next() {
		var p PlayerRecord
		if err := rows.Scan(&p.SteamID, &p.Name, &p.FirstSeen, &p.LastSeen, &p.Sessions); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
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
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.At, &e.UserID, &e.Username, &e.Instance, &e.Action, &e.Detail, &e.IP); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
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
	rows, err := s.DB.QueryContext(ctx, `SELECT at - at % ? AS t, MAX(players), AVG(out_kbs), MAX(out_kbs), AVG(in_kbs), MAX(in_kbs), MAX(ping_ms),
		MAX(queued_kb), MAX(queue_ms), SUM(failed_n), SUM(skipped_n), SUM(dropped_n), MAX(busy_pct), MAX(pass_ms), MAX(gap_ms)
		FROM (SELECT *, failed - LAG(failed) OVER run_order AS failed_n, skipped - LAG(skipped) OVER run_order AS skipped_n,
			dropped - LAG(dropped) OVER run_order AS dropped_n
			FROM net_samples WHERE instance_id = ? AND at >= ? WINDOW run_order AS (PARTITION BY run ORDER BY at))
		GROUP BY t ORDER BY t`, bucket, instanceID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NetPoint{}
	for rows.Next() {
		var p NetPoint
		if err := rows.Scan(&p.At, &p.Players, &p.Out, &p.OutMax, &p.In, &p.InMax, &p.Ping,
			&p.Queued, &p.QueueMs, &p.Failed, &p.Skipped, &p.Dropped, &p.Busy, &p.PassMs, &p.GapMs); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
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
	rows, err := s.DB.QueryContext(ctx, `SELECT at - at % ? AS t, run, AVG(cpu), MAX(cpu), CAST(AVG(mem) AS INTEGER), MAX(players)
		FROM perf_samples WHERE instance_id = ? AND at >= ? GROUP BY t, run ORDER BY t, run`, bucket, instanceID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PerfPoint{}
	for rows.Next() {
		var p PerfPoint
		if err := rows.Scan(&p.At, &p.Run, &p.CPU, &p.CPUMax, &p.Mem, &p.Players); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
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
	rows, err := s.DB.QueryContext(ctx, `SELECT run, MIN(at), MAX(at), AVG(cpu), MAX(cpu), CAST(AVG(mem) AS INTEGER), MAX(mem), MAX(players)
		FROM perf_samples WHERE instance_id = ? AND at >= ? GROUP BY run ORDER BY run DESC LIMIT ?`, instanceID, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PerfRun{}
	for rows.Next() {
		var r PerfRun
		if err := rows.Scan(&r.Run, &r.First, &r.Last, &r.CPU, &r.CPUMax, &r.Mem, &r.MemMax, &r.Players); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
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
	rows, err := s.DB.QueryContext(ctx, `SELECT instance_id, at - at % ? AS t, SUM(cpu), MAX(cpu), SUM(mem), MAX(mem), COUNT(*)
		FROM perf_samples WHERE at >= ? GROUP BY instance_id, t ORDER BY instance_id, t`, bucket, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ServerPerfPoint{}
	for rows.Next() {
		var p ServerPerfPoint
		if err := rows.Scan(&p.Instance, &p.At, &p.CPU, &p.CPUMax, &p.Mem, &p.MemMax, &p.Samples); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
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
	rows, err := s.DB.QueryContext(ctx, `SELECT at - at % ? AS t, AVG(cpu), MAX(cpu), CAST(AVG(mem_total) AS INTEGER), CAST(AVG(mem_used) AS INTEGER),
		CAST(AVG(mem_cached) AS INTEGER), MAX(limit_max), CAST(AVG(limit_used) AS INTEGER), AVG(servers_cpu), CAST(AVG(servers_mem) AS INTEGER),
		CAST(AVG(manager_mem) AS INTEGER), MAX(running), MAX(servers), MAX(players), COUNT(*)
		FROM host_samples WHERE at >= ? GROUP BY t ORDER BY t`, bucket, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HostPoint{}
	for rows.Next() {
		var p HostPoint
		if err := rows.Scan(&p.At, &p.CPU, &p.CPUMax, &p.MemTotal, &p.MemUsed, &p.MemCached, &p.LimitMax, &p.LimitUsed, &p.ServersCPU, &p.ServersMem,
			&p.ManagerMem, &p.Running, &p.Servers, &p.Players, &p.Samples); err != nil {
			return nil, err
		}
		out = append(out, p)
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
