package backup

import (
	"archive/zip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"

	"github.com/xThrasherrr/ReSkateManager/internal/config"
	"github.com/xThrasherrr/ReSkateManager/internal/safezip"
	"github.com/xThrasherrr/ReSkateManager/internal/serverconfig"
	"github.com/xThrasherrr/ReSkateManager/internal/store"
)

// maxFiles caps the files a backup may hold, as a check on one made by hand.
var maxFiles = 200000

// maxList caps the bytes of a backup's list of files, which is read whole
// before maxFiles can be checked: room for maxFiles long names.
const maxList = 32 << 20

// Reader is an open backup or export.
type Reader struct {
	Manifest
	zr     *safezip.ReadCloser
	files  map[string]*zip.File
	budget int64 // bytes it may still unpack to
}

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// OpenFile opens a backup or export and reads what it holds. Since an import
// can be any zip, nothing in it is trusted: names that could land outside
// where they go fail the restore, and it may unpack to only a few times its
// size.
func OpenFile(path string) (*Reader, error) {
	zr, err := safezip.Open(path, maxList)
	if errors.Is(err, zip.ErrInsecurePath) {
		err = nil // the reader still works; each name is checked before it's used
	}
	if errors.Is(err, safezip.ErrTooMany) {
		return nil, fmt.Errorf("it holds more than %d files", maxFiles)
	}
	if err != nil {
		return nil, fmt.Errorf("not a zip file: %w", err)
	}
	r := &Reader{zr: zr, files: make(map[string]*zip.File, len(zr.File))}
	if err := r.load(path); err != nil {
		zr.Close()
		return nil, err
	}
	return r, nil
}

func (r *Reader) load(path string) error {
	if len(r.zr.File) > maxFiles {
		return fmt.Errorf("it holds more than %d files", maxFiles)
	}
	for _, f := range r.zr.File {
		r.files[f.Name] = f
	}
	mf := r.files[manifestName]
	if mf == nil {
		return errors.New("it isn't a backup or export from ReSkateManager: it has no backup.json")
	}
	data, err := readSmall(mf, 16<<20)
	if err != nil {
		return fmt.Errorf("backup.json: %w", err)
	}
	if err := json.Unmarshal(data, &r.Manifest); err != nil {
		return fmt.Errorf("backup.json is not valid: %w", err)
	}
	if r.Format < 1 {
		return errors.New("backup.json has no format")
	}
	if r.Format > Format {
		return fmt.Errorf("it was made by a newer manager (format %d); update this one first", r.Format)
	}
	seen := map[string]bool{}
	for _, srv := range r.Servers {
		if !idRe.MatchString(srv.ID) || seen[srv.ID] {
			return fmt.Errorf("backup.json names a server %q twice, or wrongly", srv.ID)
		}
		seen[srv.ID] = true
	}
	if r.Servers == nil {
		r.Servers = []Server{}
	}
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	// Mods are stored as they are and the rest compresses a few times over at
	// most, so a backup that unpacks to much more is a zip bomb.
	r.budget = 4*fi.Size() + 1<<30
	return nil
}

// Close closes the backup's file.
func (r *Reader) Close() error { return r.zr.Close() }

// Server finds server id in the backup.
func (r *Reader) Server(id string) (*Server, bool) {
	for i := range r.Servers {
		if r.Servers[i].ID == id {
			return &r.Servers[i], true
		}
	}
	return nil, false
}

// RestoreConfig writes server id's config files from the backup into dir,
// each replaced in one step, and reports which it wrote. One the backup
// lacks is left as it is.
func (r *Reader) RestoreConfig(id, dir string) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var done []string
	for _, name := range ConfigFiles {
		f := r.files[serverPrefix(id)+name]
		if f == nil {
			continue
		}
		data, err := readSmall(f, 64<<20)
		if err != nil {
			return done, fmt.Errorf("%s: %w", name, err)
		}
		// It can hold the server's password and Steam token: for the
		// manager's user, who runs the server too.
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return done, err
		}
		if err := writeFileAtomic(path, data, 0o600); err != nil {
			return done, err
		}
		done = append(done, name)
	}
	return done, nil
}

// RestoreMods replaces server id's own mods in dir with the backup's, each
// enabled or disabled as it was, and reports how many it put in. Links, such
// as those to the shared mods, stay unless the backup has a mod of the same
// name. The backup's mods are unpacked beside the server's first and swapped
// in by renaming, so the server never sees half of them; when the swap fails
// part way, the server's own go back.
func (r *Reader) RestoreMods(ctx context.Context, id, dir string, fn func(done, total int64)) (int, error) {
	srv, ok := r.Server(id)
	if !ok || !srv.ModFiles {
		return 0, errors.New("the backup doesn't hold this server's mods")
	}
	files, err := r.under(serverPrefix(id))
	if err != nil {
		return 0, err
	}
	picked := map[string]*zip.File{}
	coming := map[string]bool{} // folder names, folded
	for rel, f := range files {
		if _, folder, ok := modPath(rel); ok {
			picked[rel] = f
			coming[fold(folder)] = true
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	tmp, err := os.MkdirTemp(dir, ".restore-")
	if err != nil {
		return 0, err
	}
	keep := false // set when tmp holds the server's own mods, which an undo couldn't put back
	defer func() {
		if !keep {
			os.RemoveAll(tmp) // RemoveAll removes a link, never what it leads to
		}
	}()
	if err := r.unpack(ctx, picked, filepath.Join(tmp, "new"), fn); err != nil {
		return 0, err
	}

	type move struct{ from, to string }
	var moved []move
	var undoErr error
	undo := func() {
		for _, m := range slices.Backward(moved) {
			if err := os.Rename(m.to, m.from); err != nil {
				undoErr = errors.Join(undoErr, err)
			}
		}
		if undoErr != nil {
			keep = true
			undoErr = fmt.Errorf("and some of the server's own mods couldn't be put back; they are in %s: %w", filepath.Join(tmp, "old"), undoErr)
		}
	}
	rename := func(from, to string) error {
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			return err
		}
		if err := os.Rename(from, to); err != nil {
			return fmt.Errorf("cannot move %s (a running server may be using it): %w", filepath.Base(from), err)
		}
		moved = append(moved, move{from, to})
		return nil
	}
	// The server's own mods go aside, and links in the way of the backup's.
	for _, sub := range modDirs {
		list, err := os.ReadDir(filepath.Join(dir, sub))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			undo()
			return 0, errors.Join(err, undoErr)
		}
		for _, e := range list {
			if !e.IsDir() && !coming[fold(e.Name())] {
				continue
			}
			if err := rename(filepath.Join(dir, sub, e.Name()), filepath.Join(tmp, "old", sub, e.Name())); err != nil {
				undo()
				return 0, errors.Join(err, undoErr)
			}
		}
	}
	for _, sub := range modDirs {
		list, err := os.ReadDir(filepath.Join(tmp, "new", sub))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			undo()
			return 0, errors.Join(err, undoErr)
		}
		for _, e := range list {
			if err := rename(filepath.Join(tmp, "new", sub, e.Name()), filepath.Join(dir, sub, e.Name())); err != nil {
				undo()
				return 0, errors.Join(err, undoErr)
			}
		}
	}
	return len(coming), nil
}

// RestoreShared puts back the shared mods the backup holds that the shared
// folder lacks, and reports which. The ones it has stay as they are.
func (r *Reader) RestoreShared(ctx context.Context, shared string, fn func(done, total int64)) ([]string, error) {
	if !r.SharedFiles || shared == "" {
		return nil, nil
	}
	files, err := r.under(sharedPrefix)
	if err != nil {
		return nil, err
	}
	lib := serverconfig.ModsDir(shared)
	picked := map[string]*zip.File{}
	folders := map[string]bool{}
	for rel, f := range files {
		folder, _, ok := strings.Cut(rel, "/")
		if !ok || !serverconfig.ValidModFolder(folder) {
			continue
		}
		if _, err := os.Lstat(filepath.Join(lib, folder)); err == nil {
			continue
		}
		picked[rel] = f
		folders[folder] = true
	}
	if len(picked) == 0 {
		return nil, nil
	}
	if err := os.MkdirAll(lib, 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp(shared, ".restore-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	if err := r.unpack(ctx, picked, tmp, fn); err != nil {
		return nil, err
	}
	var done []string
	for _, folder := range sortedKeys(folders) {
		if err := os.Rename(filepath.Join(tmp, folder), filepath.Join(lib, folder)); err != nil {
			return done, err
		}
		done = append(done, folder)
	}
	return done, nil
}

// ImportServer makes server folder dir, which must not exist yet, from the
// one server the backup holds (an export, usually), and returns that server
// as the backup describes it. It's unpacked beside dir and appears in one
// step.
func (r *Reader) ImportServer(ctx context.Context, dir string, fn func(done, total int64)) (Server, error) {
	if len(r.Servers) != 1 {
		return Server{}, fmt.Errorf("it holds %d servers; export the one server to move from its Server page", len(r.Servers))
	}
	srv := r.Servers[0]
	if _, err := os.Lstat(dir); err == nil {
		return srv, fmt.Errorf("%s already exists", dir)
	}
	files, err := r.under(serverPrefix(srv.ID))
	if err != nil {
		return srv, err
	}
	picked := map[string]*zip.File{}
	for rel, f := range files {
		if _, _, ok := modPath(rel); ok || slices.Contains(ConfigFiles, rel) {
			picked[rel] = f
		}
	}
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return srv, err
	}
	tmp, err := os.MkdirTemp(parent, ".importing-")
	if err != nil {
		return srv, err
	}
	defer os.RemoveAll(tmp)                      // nothing, once it has become dir
	if err := os.Chmod(tmp, 0o755); err != nil { // MkdirTemp makes it private
		return srv, err
	}
	if err := r.unpack(ctx, picked, tmp, fn); err != nil {
		return srv, err
	}
	return srv, os.Rename(tmp, dir)
}

// Report is what RestoreAll put back.
type Report struct {
	Safety  string   // the backup of the database it replaced, if there was one
	Servers []string // the servers it restored, by name
	Moved   []string // the servers whose folder moved with the manager's
	Shared  []string // the shared mods it put back
	Notes   []string // what the restored settings do that the person restoring should know
}

// RestoreAll puts a whole backup back while the manager is stopped: its
// manager.db and manager.toml, then each server's config and mods, then the
// shared mods that are missing. The database it replaces is backed up into
// Dir first. A server whose folder was in the backup's servers folder goes
// to the same place in this manager's, which may be on another OS.
func (s *Service) RestoreAll(ctx context.Context, zipPath string) (Report, error) {
	var rep Report
	r, err := OpenFile(zipPath)
	if err != nil {
		return rep, err
	}
	defer r.Close()
	if !r.Database {
		return rep, errors.New("this backup doesn't hold the database; restore its servers from the panel instead")
	}
	if latest, err := store.Latest(); err == nil && r.Schema > latest {
		return rep, fmt.Errorf("it was made by a newer manager (database schema %d; this one knows up to %d); restore it with that version", r.Schema, latest)
	}
	dbPath := filepath.Join(s.DataDir, "manager.db")
	// Checked before it replaces anything: whole, a schema this manager knows,
	// and without the sessions it held, some of which may have been ended
	// (a password changed, a user disabled) since the backup was made.
	incoming := dbPath + ".restore"
	if err := r.extract(ctx, dbName, incoming); err != nil {
		return rep, fmt.Errorf("manager.db: %w", err)
	}
	defer os.Remove(incoming)
	if err := checkRestoredDB(incoming); err != nil {
		return rep, fmt.Errorf("manager.db: %w", err)
	}
	if _, err := os.Stat(dbPath); err == nil {
		old, err := sql.Open("sqlite", store.DSN(dbPath, "busy_timeout(5000)"))
		if err != nil {
			return rep, err
		}
		info, err := s.create(ctx, old, Options{Kind: Restore, Database: true})
		old.Close()
		if err != nil {
			return rep, fmt.Errorf("back up the current database first: %w", err)
		}
		rep.Safety = info.Name
	}
	if err := os.MkdirAll(s.DataDir, 0o700); err != nil {
		return rep, err
	}
	// Left in place, the old database's journal would be played into the new one.
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(dbPath + suffix); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return rep, fmt.Errorf("is the manager still running? %w", err)
		}
	}
	if err := os.Rename(incoming, dbPath); err != nil {
		return rep, fmt.Errorf("manager.db: %w", err)
	}
	if r.files[tomlName] != nil {
		tomlPath := filepath.Join(s.DataDir, "manager.toml")
		if err := r.extract(ctx, tomlName, tomlPath); err != nil {
			return rep, fmt.Errorf("manager.toml: %w", err)
		}
		rep.Notes = append(rep.Notes, settingsNotes(tomlPath)...)
	}

	st, err := store.Open(dbPath)
	if err != nil {
		return rep, fmt.Errorf("the restored database: %w", err)
	}
	defer st.Close()
	defs, err := st.Instances(ctx)
	if err != nil {
		return rep, err
	}
	for _, d := range defs {
		srv, ok := r.Server(d.ID)
		if !ok {
			continue
		}
		if dir, ok := movedDir(d.Dir, r.Root, s.Root); ok && dir != d.Dir {
			if err := st.MoveInstance(ctx, d.ID, dir); err != nil {
				return rep, err
			}
			d.Dir = dir
			rep.Moved = append(rep.Moved, d.Name)
		}
		if _, err := r.RestoreConfig(d.ID, d.Dir); err != nil {
			return rep, fmt.Errorf("%s: %w", d.Name, err)
		}
		if srv.ModFiles {
			if _, err := r.RestoreMods(ctx, d.ID, d.Dir, nil); err != nil {
				return rep, fmt.Errorf("%s: %w", d.Name, err)
			}
		}
		rep.Servers = append(rep.Servers, d.Name)
	}
	if rep.Shared, err = r.RestoreShared(ctx, s.SharedDir, nil); err != nil {
		return rep, fmt.Errorf("shared mods: %w", err)
	}
	return rep, nil
}

// movedDir is where server folder dir goes when a backup made in manager
// folder from is restored into one at to: the same place under to/servers
// for a folder that was under from/servers. The two may be on different
// OSes, so it reads either kind of path.
func movedDir(dir, from, to string) (string, bool) {
	if from == "" || to == "" {
		return "", false
	}
	norm := func(p string) string { return strings.TrimRight(strings.ReplaceAll(p, `\`, "/"), "/") }
	d, base := norm(dir), norm(from)+"/servers/"
	if len(from) >= 2 && from[1] == ':' || strings.HasPrefix(from, `\\`) { // Windows names fold case
		if !strings.HasPrefix(strings.ToLower(d), strings.ToLower(base)) {
			return "", false
		}
	} else if !strings.HasPrefix(d, base) {
		return "", false
	}
	rest := d[len(base):]
	if cleanRel(rest) == "" {
		return "", false
	}
	return filepath.Join(to, "servers", filepath.FromSlash(rest)), true
}

// under lists the files in the zip whose names start with prefix, by their
// path below it. A name that could land outside fails the lot.
func (r *Reader) under(prefix string) (map[string]*zip.File, error) {
	out := map[string]*zip.File{}
	for _, f := range r.zr.File {
		if !strings.HasPrefix(f.Name, prefix) || !f.Mode().IsRegular() {
			continue // folders are made as needed; links are never followed
		}
		if cleanRel(f.Name) == "" {
			return nil, fmt.Errorf("it holds an unsafe path: %q", f.Name)
		}
		out[strings.TrimPrefix(f.Name, prefix)] = f
	}
	return out, nil
}

// modPath reports whether rel, a path in a server's part of a backup, is a
// file in a mod's folder, and which: Mods/<folder>/... or DisabledMods/...
func modPath(rel string) (sub, folder string, ok bool) {
	sub, rest, ok1 := strings.Cut(rel, "/")
	folder, _, ok2 := strings.Cut(rest, "/")
	return sub, folder, ok1 && ok2 && slices.Contains(modDirs, sub) && serverconfig.ValidModFolder(folder)
}

// unpack writes files, by their paths below dir, into dir.
func (r *Reader) unpack(ctx context.Context, files map[string]*zip.File, dir string, fn func(done, total int64)) error {
	p := &progress{fn: fn}
	for _, f := range files {
		p.total += int64(f.UncompressedSize64)
	}
	for _, rel := range sortedKeys(files) {
		if err := r.unzip(ctx, files[rel], filepath.Join(dir, filepath.FromSlash(rel)), p); err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
	}
	p.finish()
	return nil
}

// extract writes one file of the zip over the file at path, in one step.
func (r *Reader) extract(ctx context.Context, name, path string) error {
	f := r.files[name]
	if f == nil {
		return errors.New("not in the backup")
	}
	tmp := path + ".part"
	os.Remove(tmp)
	if err := r.unzip(ctx, f, tmp, &progress{}); err != nil {
		os.Remove(tmp)
		return err
	}
	// The database holds password hashes, manager.toml the panel's address:
	// for the manager's user alone, and on disk before they take over.
	if err := syncPrivate(tmp); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// syncPrivate makes a file the manager's user's alone and flushes it to disk.
func syncPrivate(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	err = f.Chmod(0o600)
	if serr := f.Sync(); err == nil {
		err = serr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// writeFileAtomic replaces path with data in one step: written beside it,
// flushed to disk, then renamed over it. A crash leaves the old file or the
// new one, never half of either.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name()) // gone already, once renamed
	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(perm)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// checkRestoredDB checks a database about to be restored: whole, with a
// schema this manager knows; and ends the sessions it holds.
func checkRestoredDB(path string) error {
	db, err := sql.Open("sqlite", store.DSN(path))
	if err != nil {
		return err
	}
	defer db.Close()
	var result string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("it is damaged: %s", result)
	}
	schema, err := store.Schema(db)
	if err != nil {
		return err
	}
	if latest, err := store.Latest(); err == nil && schema > latest {
		return fmt.Errorf("it is from a newer manager (schema %d; this one knows up to %d)", schema, latest)
	}
	if schema > 0 {
		if _, err := db.Exec("DELETE FROM sessions"); err != nil {
			return err
		}
	}
	return nil
}

// settingsNotes says what a restored manager.toml does that someone
// restoring a backup they didn't make should look at: where updates come
// from, which the manager installs and runs.
func settingsNotes(path string) []string {
	c, err := config.Load(path)
	if err != nil {
		return []string{"its manager.toml can't be read (" + err.Error() + "); fix or delete data/manager.toml before starting"}
	}
	d := config.Default()
	var notes []string
	if c.UpdateRepo != d.UpdateRepo {
		notes = append(notes, "server updates come from github.com/"+c.UpdateRepo+", not "+d.UpdateRepo+" (update_repo in data/manager.toml)")
	}
	if c.ManagerRepo != d.ManagerRepo && c.ManagerRepo != "" {
		notes = append(notes, "manager updates come from github.com/"+c.ManagerRepo+", not "+d.ManagerRepo+" (manager_repo in data/manager.toml)")
	}
	return notes
}

// unzip writes f to a new file at path, out of the backup's budget.
func (r *Reader) unzip(ctx context.Context, f *zip.File, path string, p *progress) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	n, err := copyCtx(ctx, out, io.LimitReader(rc, r.budget+1), p)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n > r.budget {
		return errors.New("it unpacks to far more than its size; the zip may be damaged")
	}
	r.budget -= n
	if !f.Modified.IsZero() {
		_ = os.Chtimes(path, f.Modified, f.Modified)
	}
	return nil
}

// readSmall reads a file in the zip that must stay under limit bytes.
func readSmall(f *zip.File, limit int64) ([]byte, error) {
	if f.UncompressedSize64 > uint64(limit) {
		return nil, fmt.Errorf("over %d MB", limit>>20)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err == nil && int64(len(data)) > limit {
		err = fmt.Errorf("over %d MB", limit>>20)
	}
	return data, err
}

// fold makes names the file system takes for the same one equal.
func fold(name string) string {
	if runtime.GOOS == "windows" {
		return strings.ToLower(name)
	}
	return name
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
