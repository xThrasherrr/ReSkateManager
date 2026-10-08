// Package backup keeps copies of what the manager can't download again, as
// zip files in backups/: its database and settings file, and each server's
// config and world layers, with the mods when asked. It puts a server's files
// back from one, and moves a server to another manager as an export.
package backup

import (
	"archive/zip"
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/instance"
	"github.com/xThrasherrr/ReSkateManager/internal/serverconfig"
	"github.com/xThrasherrr/ReSkateManager/internal/store"
)

// Format is the version of the layout below. A reader refuses a newer one.
const Format = 1

// Inside a backup, files sit where they sit under the manager's folder, with
// servers by ID, and backup.json says what it holds:
//
//	backup.json
//	data/manager.db
//	data/manager.toml
//	servers/<id>/ReSkateServer.json
//	servers/<id>/world-layers.json
//	servers/<id>/Mods/<folder>/...
//	servers/<id>/DisabledMods/<folder>/...
//	shared/Mods/<folder>/...
const (
	manifestName = "backup.json"
	dbName       = "data/manager.db"
	tomlName     = "data/manager.toml"
	sharedPrefix = "shared/Mods/"
)

func serverPrefix(id string) string { return "servers/" + id + "/" }

// ConfigFiles are the files in a server's folder that a backup holds: what
// it can't download again, apart from its mods. A server update leaves both
// in place.
var ConfigFiles = []string{"ReSkateServer.json", "world-layers.json"}

// modDirs are a server's folders of mods, enabled and disabled.
var modDirs = []string{"Mods", "DisabledMods"}

// Kind is why a backup was made: by hand, on schedule, or before a change.
type Kind string

const (
	Manual        Kind = "manual"         // made from the panel
	Scheduled     Kind = "scheduled"      // made on the schedule
	ServerUpdate  Kind = "server-update"  // before a server update: that server's config
	ManagerUpdate Kind = "manager-update" // before the manager updates itself
	Migration     Kind = "migration"      // before the database's schema changes: the database alone
	Restore       Kind = "restore"        // before --restore replaced the database
	Export        Kind = "export"         // one server, to import on another manager; never kept in backups/
)

// Manifest is backup.json: what a backup holds.
type Manifest struct {
	Format  int    `json:"format"`
	Kind    Kind   `json:"kind"`
	Created int64  `json:"created"` // unix seconds
	Version string `json:"version"` // of the manager that made it
	By      string `json:"by,omitempty"`
	// Root is the manager's folder, so a restore into another one can move
	// the servers kept under it along.
	Root     string   `json:"root,omitempty"`
	Database bool     `json:"database"`         // holds manager.db and manager.toml
	Schema   int      `json:"schema,omitempty"` // manager.db's schema version
	Servers  []Server `json:"servers"`
	// The shared mods there were, and whether it holds their files.
	Shared      []Mod `json:"shared,omitempty"`
	SharedFiles bool  `json:"sharedFiles,omitempty"`
}

// Server is one server in a backup: its settings in the panel, and which of
// its files it holds.
type Server struct {
	instance.Def
	Files []string `json:"files"` // of ConfigFiles
	Mods  []Mod    `json:"mods"`  // every mod it had
	// ModFiles: it holds the files of the server's own mods. An export holds
	// the shared mods it loaded too, as its own.
	ModFiles bool `json:"modFiles"`
	// Its announcements, in an export.
	Announcements []store.Announcement `json:"announcements,omitempty"`
}

// Mod is one mod a server or the shared mods had.
type Mod struct {
	Folder   string `json:"folder"`
	Title    string `json:"title,omitempty"`
	Version  string `json:"version,omitempty"`
	Package  string `json:"package,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
	Shared   bool   `json:"shared,omitempty"`
}

// Info is a backup in backups/, as the panel lists them.
type Info struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	Manifest
	Error string `json:"error,omitempty"` // why it can't be read
}

// Service makes, lists and prunes the backups in Dir, and restores them.
type Service struct {
	Dir       string // backups/
	DataDir   string // data/, with manager.toml
	Root      string // the manager's folder
	SharedDir string // the shared mods' folder; "" with them off
	Version   string
	Store     *store.Store
	Reg       *instance.Registry
	Log       *slog.Logger

	mu      sync.Mutex // one backup at a time
	cacheMu sync.Mutex
	cache   map[string]cached // manifests read, by file name
}

type cached struct {
	size int64
	mod  time.Time
	info Info
}

// Options say what a backup holds.
type Options struct {
	Kind     Kind
	By       string   // who asked; "" for the manager itself
	Database bool     // manager.db and manager.toml
	Servers  []string // the servers it holds, by ID
	// Mods adds the files of the servers' own mods, and of the shared mods.
	Mods     bool
	Progress func(done, total int64)
}

// Create writes a backup into Dir. After an automatic one, the oldest of its
// kind past the number kept are deleted.
func (s *Service) Create(ctx context.Context, opt Options) (Info, error) {
	var db *sql.DB
	if opt.Database {
		db = s.Store.DB
	}
	info, err := s.create(ctx, db, opt)
	if err == nil && opt.Kind != Manual {
		s.prune(ctx)
	}
	return info, err
}

// BeforeMigration backs up the database alone, before its schema changes
// from version from. It runs while the manager starts, before Store is set.
func (s *Service) BeforeMigration(db *sql.DB, from int) error {
	info, err := s.create(context.Background(), db, Options{Kind: Migration, Database: true})
	if err == nil && s.Log != nil {
		s.Log.Info("backed up the database before upgrading it", "file", info.Name, "schema", from)
	}
	return err
}

func (s *Service) create(ctx context.Context, db *sql.DB, opt Options) (Info, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// It holds the database, with password hashes: like data/, for the
	// manager's user alone, however it was made.
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return Info{}, err
	}
	if err := os.Chmod(s.Dir, 0o700); err != nil {
		return Info{}, err
	}
	now := time.Now()
	m := Manifest{Format: Format, Kind: opt.Kind, Created: now.Unix(), Version: s.Version, By: opt.By, Root: s.Root, Servers: []Server{}}
	var entries []entry
	if db != nil {
		tmp := filepath.Join(s.Dir, ".manager-"+rand.Text()+".db")
		defer os.Remove(tmp)
		if err := vacuumInto(ctx, db, tmp); err != nil {
			return Info{}, fmt.Errorf("copy the database: %w", err)
		}
		m.Database = true
		m.Schema, _ = store.Schema(db)
		entries = appendFile(entries, dbName, tmp, false)
		entries = appendFile(entries, tomlName, filepath.Join(s.DataDir, "manager.toml"), false)
	}
	for _, id := range opt.Servers {
		in, ok := s.Reg.Get(id)
		if !ok {
			continue // removed meanwhile
		}
		srv, es, err := collectServer(in.Def(), opt.Mods, false)
		if err != nil {
			return Info{}, fmt.Errorf("%s: %w", in.Def().Name, err)
		}
		m.Servers = append(m.Servers, srv)
		entries = append(entries, es...)
	}
	if opt.Mods && s.SharedDir != "" {
		mods, es, err := collectShared(s.SharedDir)
		if err != nil {
			return Info{}, fmt.Errorf("shared mods: %w", err)
		}
		m.Shared, m.SharedFiles = mods, true
		entries = append(entries, es...)
	}

	server := ""
	if opt.Kind == ServerUpdate && len(m.Servers) == 1 {
		server = m.Servers[0].ID
	}
	name := s.freeName(opt.Kind, now, server)
	part, err := os.CreateTemp(s.Dir, ".partial-*.zip")
	if err != nil {
		return Info{}, err
	}
	defer os.Remove(part.Name()) // gone already, once renamed
	p := newProgress(entries, opt.Progress)
	err = writeZip(ctx, part, m, entries, p)
	if err == nil {
		err = part.Sync() // whole on disk before it's listed as a backup
	}
	if cerr := part.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return Info{}, err
	}
	if err := os.Rename(part.Name(), filepath.Join(s.Dir, name)); err != nil {
		return Info{}, err
	}
	p.finish()
	fi, err := os.Stat(filepath.Join(s.Dir, name))
	if err != nil {
		return Info{}, err
	}
	info := Info{Name: name, Size: fi.Size(), Manifest: m}
	s.remember(name, fi, info)
	return info, nil
}

// WriteExport writes server def's files, and its announcements, as a zip to w,
// for importing on another manager. With mods it holds every mod the server
// has, shared ones included, since the other manager doesn't have them.
func WriteExport(ctx context.Context, w io.Writer, def instance.Def, mods, redact bool, ann []store.Announcement, version string) error {
	srv, entries, err := collectServer(def, mods, true)
	if err != nil {
		return err
	}
	if redact {
		if err := redactConfig(entries, serverPrefix(def.ID)+"ReSkateServer.json"); err != nil {
			return err
		}
	}
	srv.Announcements = ann
	m := Manifest{Format: Format, Kind: Export, Created: time.Now().Unix(), Version: version, Servers: []Server{srv}}
	return writeZip(ctx, w, m, entries, newProgress(entries, nil))
}

func vacuumInto(ctx context.Context, db *sql.DB, to string) error {
	// It refuses to write over a file, so a name taken meanwhile fails safe.
	_, err := db.ExecContext(ctx, "VACUUM INTO ?", to)
	return err
}

// freeName names a new backup after its kind and time, and the server it's
// for when there's one.
func (s *Service) freeName(kind Kind, at time.Time, server string) string {
	base := "backup-" + at.Format("20060102-150405") + "-" + string(kind)
	if server != "" {
		base += "-" + server
	}
	name := base + ".zip"
	for i := 2; ; i++ {
		if _, err := os.Lstat(filepath.Join(s.Dir, name)); errors.Is(err, fs.ErrNotExist) {
			return name
		}
		name = fmt.Sprintf("%s-%d.zip", base, i)
	}
}

// entry is a file to put in a backup.
type entry struct {
	name string // in the zip
	path string // on disk
	size int64
	mod  time.Time
	raw  bool   // stored as it is: mods are mostly compressed already
	data []byte // written in place of the file at path, when set
}

// redactConfig puts a copy of the config without its owner-only settings
// in place of the file, for an export by someone who isn't an owner.
func redactConfig(entries []entry, name string) error {
	i := slices.IndexFunc(entries, func(e entry) bool { return e.name == name })
	if i < 0 {
		return nil
	}
	f, err := serverconfig.Read(entries[i].path)
	if err != nil {
		return err
	}
	f.Redact()
	data, err := f.Bytes()
	if err != nil {
		return err
	}
	entries[i].data, entries[i].size = data, int64(len(data))
	return nil
}

// appendFile adds the file at path, if there is one.
func appendFile(entries []entry, name, path string, raw bool) []entry {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return entries
	}
	return append(entries, entry{name: name, path: path, size: fi.Size(), mod: fi.ModTime(), raw: raw})
}

// collectServer lists what a backup of server def holds. With mods it holds
// the files of the server's own mods; with export too, those of the mods it
// links to, such as the shared ones. A backup leaves links be: it holds the
// shared mods once, beside the servers.
func collectServer(def instance.Def, mods, export bool) (Server, []entry, error) {
	srv := Server{Def: def, Files: []string{}, Mods: []Mod{}, ModFiles: mods}
	prefix := serverPrefix(def.ID)
	var entries []entry
	for _, name := range ConfigFiles {
		n := len(entries)
		if entries = appendFile(entries, prefix+name, filepath.Join(def.Dir, name), false); len(entries) > n {
			srv.Files = append(srv.Files, name)
		}
	}
	list, err := serverconfig.ReadMods(def.Dir)
	if err != nil {
		return srv, nil, err
	}
	for _, m := range list {
		srv.Mods = append(srv.Mods, modOf(m))
	}
	if !mods {
		return srv, entries, nil
	}
	for _, sub := range modDirs {
		parent := filepath.Join(def.Dir, sub)
		dirs, err := os.ReadDir(parent)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return srv, nil, err
		}
		for _, e := range dirs {
			p := filepath.Join(parent, e.Name())
			if !serverconfig.ValidModFolder(e.Name()) {
				continue
			}
			if !e.IsDir() {
				// A link (a junction on Windows). It counts only in an export,
				// and only when it leads to a folder.
				if fi, err := os.Stat(p); !export || err != nil || !fi.IsDir() {
					continue
				}
			}
			if entries, err = appendTree(entries, p, prefix+sub+"/"+e.Name()+"/"); err != nil {
				return srv, nil, err
			}
		}
	}
	if export {
		// The other manager gets them as the server's own.
		for i := range srv.Mods {
			srv.Mods[i].Shared = false
		}
	}
	return srv, entries, nil
}

// collectShared lists the shared mods and their files.
func collectShared(shared string) ([]Mod, []entry, error) {
	list, err := serverconfig.ReadMods(shared)
	if err != nil {
		return nil, nil, err
	}
	mods := []Mod{}
	var entries []entry
	for _, m := range list {
		if m.Disabled {
			continue // the shared folder has no DisabledMods; one made by hand isn't shared
		}
		p := filepath.Join(serverconfig.ModsDir(shared), m.Folder)
		if fi, err := os.Lstat(p); err != nil || !fi.IsDir() {
			continue // a link: the shared mods hold folders
		}
		mods = append(mods, modOf(m))
		if entries, err = appendTree(entries, p, sharedPrefix+m.Folder+"/"); err != nil {
			return nil, nil, err
		}
	}
	return mods, entries, nil
}

func modOf(m serverconfig.Mod) Mod {
	return Mod{Folder: m.Folder, Title: m.Title, Version: m.Version, Package: m.Package, Disabled: m.Disabled, Shared: m.Shared}
}

// appendTree adds each file under root, as prefix plus its path below root.
func appendTree(entries []entry, root, prefix string) ([]entry, error) {
	err := walkFiles(root, func(path, rel string, fi fs.FileInfo) error {
		entries = append(entries, entry{name: prefix + rel, path: path, size: fi.Size(), mod: fi.ModTime(), raw: true})
		return nil
	})
	return entries, err
}

// walkFiles calls fn for each regular file under root, which may itself be a
// link to a folder, with its path below root in forward slashes. Links below
// root are not followed, so a link can't loop or reach outside the mod.
func walkFiles(root string, fn func(path, rel string, fi fs.FileInfo) error) error {
	var walk func(dir, rel string) error
	walk = func(dir, rel string) error {
		list, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, e := range list {
			p := filepath.Join(dir, e.Name())
			r := e.Name()
			if rel != "" {
				r = rel + "/" + r
			}
			switch {
			case e.IsDir():
				if err := walk(p, r); err != nil {
					return err
				}
			case e.Type().IsRegular():
				fi, err := e.Info()
				if errors.Is(err, fs.ErrNotExist) {
					continue // deleted meanwhile
				}
				if err != nil {
					return err
				}
				if err := fn(p, r, fi); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(root, "")
}

// writeZip writes backup.json and then each entry. A file deleted since it
// was listed is left out.
func writeZip(ctx context.Context, w io.Writer, m Manifest, entries []entry, p *progress) error {
	zw := zip.NewWriter(w)
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	f, err := zw.CreateHeader(&zip.FileHeader{Name: manifestName, Method: zip.Deflate, Modified: time.Unix(m.Created, 0)})
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	for _, e := range entries {
		if err := addEntry(ctx, zw, e, p); err != nil {
			return fmt.Errorf("%s: %w", e.name, err)
		}
	}
	return zw.Close()
}

func addEntry(ctx context.Context, zw *zip.Writer, e entry, p *progress) error {
	var src io.Reader = bytes.NewReader(e.data)
	if e.data == nil {
		f, err := os.Open(e.path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		defer f.Close()
		src = f
	}
	h := &zip.FileHeader{Name: e.name, Method: zip.Deflate, Modified: e.mod}
	if e.raw {
		h.Method = zip.Store
	}
	h.SetMode(0o644)
	dst, err := zw.CreateHeader(h)
	if err != nil {
		return err
	}
	_, err = copyCtx(ctx, dst, src, p)
	return err
}

// copyCtx copies like io.Copy, counting into p and stopping once ctx ends.
func copyCtx(ctx context.Context, dst io.Writer, src io.Reader, p *progress) (int64, error) {
	if p.buf == nil {
		p.buf = make([]byte, 1<<20)
	}
	buf := p.buf
	var n int64
	for {
		if err := ctx.Err(); err != nil {
			return n, err
		}
		k, rerr := src.Read(buf)
		if k > 0 {
			if _, err := dst.Write(buf[:k]); err != nil {
				return n, err
			}
			n += int64(k)
			p.add(int64(k))
		}
		if rerr == io.EOF {
			return n, nil
		}
		if rerr != nil {
			return n, rerr
		}
	}
}

// progress counts the bytes copied out of a known total, and reports them a
// few times a second.
type progress struct {
	done, total int64
	fn          func(done, total int64)
	last        time.Time
	buf         []byte // copyCtx's, kept across the files of one backup
}

func newProgress(entries []entry, fn func(done, total int64)) *progress {
	p := &progress{fn: fn}
	for _, e := range entries {
		p.total += e.size
	}
	return p
}

func (p *progress) add(n int64) {
	p.done += n
	if p.fn != nil && time.Since(p.last) > 250*time.Millisecond {
		p.last = time.Now()
		p.fn(p.done, max(p.total, p.done))
	}
}

func (p *progress) finish() {
	if p.fn != nil {
		p.fn(p.done, max(p.total, p.done))
	}
}

// ---- the folder of backups ----

var nameRe = regexp.MustCompile(`^backup-\d{8}-\d{6}-[a-z0-9-]+\.zip$`)

// ErrNoBackup is a name that isn't a backup in Dir.
var ErrNoBackup = errors.New("no such backup")

// Path is where backup name is, or ErrNoBackup.
func (s *Service) Path(name string) (string, error) {
	if !nameRe.MatchString(name) {
		return "", ErrNoBackup
	}
	p := filepath.Join(s.Dir, name)
	if fi, err := os.Lstat(p); err != nil || !fi.Mode().IsRegular() {
		return "", ErrNoBackup
	}
	return p, nil
}

// Open opens backup name to read, within Dir: a link put there can't lead
// out of it, and what is opened is what was checked to be a file.
func (s *Service) Open(name string) (*os.File, error) {
	if !nameRe.MatchString(name) {
		return nil, ErrNoBackup
	}
	f, err := os.OpenInRoot(s.Dir, name)
	if err != nil {
		return nil, ErrNoBackup
	}
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return nil, ErrNoBackup
	}
	return f, nil
}

// List reads the backups in Dir, newest first.
func (s *Service) List() ([]Info, error) {
	dir, err := os.ReadDir(s.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return []Info{}, nil
	}
	if err != nil {
		return nil, err
	}
	type row struct {
		info Info
		mod  time.Time
	}
	var rows []row
	seen := map[string]bool{}
	for _, e := range dir {
		if !e.Type().IsRegular() || !nameRe.MatchString(e.Name()) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		seen[e.Name()] = true
		rows = append(rows, row{s.read(e.Name(), fi), fi.ModTime()})
	}
	s.cacheMu.Lock()
	for name := range s.cache {
		if !seen[name] {
			delete(s.cache, name)
		}
	}
	s.cacheMu.Unlock()
	// Backups made within one second go by when their file was written, and
	// by name when a file system keeps times too coarse to tell them apart.
	slices.SortFunc(rows, func(a, b row) int {
		return cmp.Or(
			cmp.Compare(b.info.Created, a.info.Created),
			b.mod.Compare(a.mod),
			strings.Compare(b.info.Name, a.info.Name),
		)
	})
	out := make([]Info, len(rows))
	for i, r := range rows {
		out[i] = r.info
	}
	return out, nil
}

// read returns a backup's manifest, read once per version of the file.
func (s *Service) read(name string, fi fs.FileInfo) Info {
	s.cacheMu.Lock()
	c, ok := s.cache[name]
	s.cacheMu.Unlock()
	if ok && c.size == fi.Size() && c.mod.Equal(fi.ModTime()) {
		return c.info
	}
	info := Info{Name: name, Size: fi.Size()}
	r, err := OpenFile(filepath.Join(s.Dir, name))
	if err != nil {
		info.Error = err.Error()
		info.Created = fi.ModTime().Unix()
		info.Servers = []Server{}
	} else {
		info.Manifest = r.Manifest
		r.Close()
	}
	s.remember(name, fi, info)
	return info
}

func (s *Service) remember(name string, fi fs.FileInfo, info Info) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if s.cache == nil {
		s.cache = map[string]cached{}
	}
	s.cache[name] = cached{fi.Size(), fi.ModTime(), info}
}

// Delete removes backup name.
func (s *Service) Delete(name string) error {
	p, err := s.Path(name)
	if err != nil {
		return err
	}
	return os.Remove(p)
}

// Prune deletes the oldest automatic backups past keep of each kind (of each
// server, for server updates), and what backups cut short left behind. The
// backups people made stay until someone deletes them.
func (s *Service) Prune(keep int) ([]string, error) {
	list, err := s.List()
	if err != nil {
		return nil, err
	}
	groups := map[string][]Info{}
	for _, b := range list {
		if b.Kind == Manual || b.Error != "" {
			continue
		}
		key := string(b.Kind)
		if b.Kind == ServerUpdate && len(b.Servers) == 1 {
			key += "/" + b.Servers[0].ID
		}
		groups[key] = append(groups[key], b) // newest first, as listed
	}
	var gone []string
	var errs []error
	for _, g := range groups {
		for _, b := range g[min(max(keep, 1), len(g)):] {
			if err := s.Delete(b.Name); err != nil {
				errs = append(errs, err)
			} else {
				gone = append(gone, b.Name)
			}
		}
	}
	leftovers, _ := filepath.Glob(filepath.Join(s.Dir, ".partial-*.zip"))
	dbs, _ := filepath.Glob(filepath.Join(s.Dir, ".manager-*.db"))
	for _, p := range append(leftovers, dbs...) {
		// A backup still being written is recent.
		if fi, err := os.Stat(p); err == nil && time.Since(fi.ModTime()) > 24*time.Hour {
			os.Remove(p)
		}
	}
	slices.Sort(gone)
	return gone, errors.Join(errs...)
}

// ---- settings ----

// Settings are when scheduled backups run, and how many are kept.
type Settings struct {
	Every int  `json:"every"` // hours between scheduled backups; 0 turns them off
	Keep  int  `json:"keep"`  // how many of each kind of automatic backup are kept
	Mods  bool `json:"mods"`  // scheduled backups hold the mods' files too
}

// DefaultSettings back up once a day and keep a week's worth, without mods:
// the database and configs come to a few MB, while one map can be GBs.
var DefaultSettings = Settings{Every: 24, Keep: 7}

// MaxEvery (hours, a month) and MaxKeep bound the backup schedule.
const (
	MaxEvery = 24 * 30
	MaxKeep  = 100
)

const settingsKey = "backups"

// LoadSettings reads the backup schedule, the defaults where unset.
func LoadSettings(ctx context.Context, s *store.Store) (Settings, error) {
	v, err := s.Get(ctx, settingsKey)
	if errors.Is(err, store.ErrNotFound) {
		return DefaultSettings, nil
	}
	if err != nil {
		return DefaultSettings, err
	}
	set := DefaultSettings
	err = json.Unmarshal([]byte(v), &set)
	return set, err
}

// SaveSettings checks and saves the backup schedule.
func SaveSettings(ctx context.Context, s *store.Store, set Settings) error {
	if set.Every < 0 || set.Every > MaxEvery {
		return fmt.Errorf("back up every 1 to %d hours, or 0 for never", MaxEvery)
	}
	if set.Keep < 1 || set.Keep > MaxKeep {
		return fmt.Errorf("keep 1 to %d backups", MaxKeep)
	}
	b, err := json.Marshal(set)
	if err != nil {
		return err
	}
	return s.Set(ctx, settingsKey, string(b))
}

// cleanRel turns a name in a zip into a path below it, or "" when it's one
// that could land outside, such as an absolute one or one with "..".
func cleanRel(name string) string {
	clean, ok := serverconfig.ZipEntryPath(name)
	if !ok || clean != name || strings.HasSuffix(name, "/") {
		return ""
	}
	return clean
}
