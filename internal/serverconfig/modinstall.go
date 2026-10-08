package serverconfig

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/xThrasherrr/ReSkateManager/internal/safezip"
)

// Variables so tests can lower them.
var (
	maxModBytes int64 = 8 << 30 // unpacked, so a small zip cannot fill the disk
	maxModFiles       = 50000
	// maxModList caps the bytes of a mod zip's list of files, which is read
	// whole before maxModFiles can be checked: room for maxModFiles long names.
	maxModList int64 = 8 << 20
)

// InstallMod unpacks a mod's zip into dir/Mods. The zip may hold the mod's
// files at its root (the folder is then named after zipName), one folder with
// them inside, or Mods/<folder>/ as zipped from a game install. A folder of the
// same name is replaced, so uploading a newer version updates the mod; a
// disabled one stays disabled.
func InstallMod(dir, zipPath, zipName string) (folder string, replaced bool, err error) {
	u, err := UnpackMod(dir, zipPath, zipName)
	if err != nil {
		return "", false, err
	}
	defer u.Discard()
	return u.Install("")
}

// UpdateMod installs a newer version of the mod in folder old, which may land
// in a folder of another name (Thunderstore's carry the version), and removes
// the old one. The mod stays enabled or disabled as it was.
func UpdateMod(dir, old, zipPath, zipName string) (folder string, err error) {
	if _, _, err := findMod(dir, old); err != nil {
		return "", err
	}
	u, err := UnpackMod(dir, zipPath, zipName)
	if err != nil {
		return "", err
	}
	defer u.Discard()
	folder, _, err = u.Install(old)
	return folder, err
}

// Unpacked is a mod unpacked beside a server's Mods folder, not yet in it.
// Unpacking is the slow part of installing a mod and changes nothing the
// server sees, so it needs no lock; Install, which does, is a few renames.
type Unpacked struct {
	dir    string // the server's folder, or the shared mods'
	tmp    string // where the mod's files are
	folder string // the folder they go to
}

// UnpackMod unpacks a mod's zip, laid out as InstallMod takes it, beside
// dir/Mods. Discard it once done with it, installed or not.
func UnpackMod(dir, zipPath, zipName string) (*Unpacked, error) {
	zr, err := safezip.Open(zipPath, maxModList)
	if errors.Is(err, zip.ErrInsecurePath) {
		err = nil // the reader is still usable; each path is checked below
	}
	if errors.Is(err, safezip.ErrTooMany) {
		return nil, fmt.Errorf("the zip has more than %d files", maxModFiles)
	}
	if err != nil {
		return nil, errors.New("not a zip file")
	}
	defer zr.Close()

	files, err := modFiles(zr.Reader)
	if err != nil {
		return nil, err
	}
	prefix, folder, err := modRoot(files)
	if err != nil {
		return nil, err
	}
	if folder == "" {
		folder = strings.TrimSpace(path.Base(strings.ReplaceAll(zipName, `\`, "/")))
		if ext := path.Ext(folder); strings.EqualFold(ext, ".zip") {
			folder = strings.TrimSpace(strings.TrimSuffix(folder, ext))
		}
	}
	if !ValidModFolder(folder) {
		return nil, fmt.Errorf("%q cannot be a mod folder name; rename the zip", folder)
	}

	// Unpack beside Mods, not in it, so the server never sees half a mod.
	tmp, err := os.MkdirTemp(dir, ".mod-")
	if err != nil {
		return nil, err
	}
	u := &Unpacked{dir: dir, tmp: tmp, folder: folder}
	if err := os.Chmod(tmp, 0o755); err != nil { // MkdirTemp makes it private
		u.Discard()
		return nil, err
	}
	budget := maxModBytes
	for _, e := range files {
		to := filepath.Join(tmp, filepath.FromSlash(strings.TrimPrefix(e.name, prefix)))
		if budget, err = unzipFile(e.f, to, budget); err != nil {
			u.Discard()
			return nil, err
		}
	}
	return u, nil
}

// Folder is the folder the mod goes to.
func (u *Unpacked) Folder() string { return u.folder }

// Discard deletes the unpacked files, unless Install moved them into place.
func (u *Unpacked) Discard() { os.RemoveAll(u.tmp) }

// Install moves the mod into Mods, or DisabledMods when a disabled mod of its
// folder is there, replacing that folder. With old, it's an update of the mod
// in that folder: it lands enabled or disabled as old was, and old goes. The
// caller holds whatever keeps changes to the Mods folder one at a time.
func (u *Unpacked) Install(old string) (folder string, replaced bool, err error) {
	dir, tmp, folder := u.dir, u.tmp, u.folder
	parent := ModsDir(dir)
	if old != "" {
		if _, disabled, err := findMod(dir, old); err == nil && disabled {
			parent = DisabledModsDir(dir)
		}
	} else if fi, err := os.Stat(filepath.Join(DisabledModsDir(dir), folder)); err == nil && fi.IsDir() {
		parent = DisabledModsDir(dir)
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", false, err
	}
	final := filepath.Join(parent, folder)
	if fi, err := os.Lstat(final); err == nil {
		if isSharedLink(final) {
			return "", false, fmt.Errorf("%s comes from the shared mods on this server; update it there", folder)
		}
		if !fi.IsDir() {
			return "", false, fmt.Errorf("%s already has a file named %q", filepath.Base(parent), folder)
		}
		prev := tmp + ".old"
		if err := os.Rename(final, prev); err != nil {
			return "", false, fmt.Errorf("cannot replace %s: %w", folder, err)
		}
		if err := os.Rename(tmp, final); err != nil {
			if rerr := os.Rename(prev, final); rerr != nil {
				// The mod as it was is still whole, just not where the server looks.
				return "", false, fmt.Errorf("cannot replace %s: %w; its old copy is in %s", folder, err, prev)
			}
			return "", false, fmt.Errorf("cannot replace %s: %w", folder, err)
		}
		os.RemoveAll(prev)
		replaced = true
	} else if err := os.Rename(tmp, final); err != nil {
		return "", false, err
	}
	if old != "" && !strings.EqualFold(old, folder) {
		if err := DeleteMod(dir, old); err != nil {
			return folder, replaced, fmt.Errorf("installed %s but could not remove %s: %w", folder, old, err)
		}
	}
	return folder, replaced, nil
}

type zipEntry struct {
	name string // cleaned, with forward slashes
	f    *zip.File
}

// modFiles lists the files in a mod's zip to unpack. A path that could land
// outside the mod's folder fails the whole zip.
func modFiles(zr *zip.Reader) ([]zipEntry, error) {
	var files []zipEntry
	for _, f := range zr.File {
		if !f.Mode().IsRegular() {
			continue // folders are made as needed; links are never followed
		}
		name, ok := ZipEntryPath(f.Name)
		if !ok {
			return nil, fmt.Errorf("the zip has an unsafe path: %q", f.Name)
		}
		if strings.HasPrefix(name, "__MACOSX/") || path.Base(name) == ".DS_Store" {
			continue
		}
		files = append(files, zipEntry{name, f})
	}
	if len(files) == 0 {
		return nil, errors.New("the zip has no files")
	}
	if len(files) > maxModFiles {
		return nil, fmt.Errorf("the zip has more than %d files", maxModFiles)
	}
	// Each file's size is in the zip's list, and reading one gives no more:
	// archive/zip fails a file that unpacks past the size it was listed with.
	// So the whole mod can be refused before anything reaches the disk.
	var total uint64
	for _, e := range files {
		total += e.f.UncompressedSize64
		if total > uint64(maxModBytes) {
			return nil, fmt.Errorf("the mod unpacks to more than %d GB", maxModBytes>>30)
		}
	}
	return files, nil
}

// modRoot finds where a mod's files start in its zip: at the root, in the
// one folder that holds everything, or in Mods/<folder>/ as zipped from a
// game install. folder is the folder the zip names, or "" for none.
func modRoot(files []zipEntry) (prefix, folder string, err error) {
	names := make([]string, len(files))
	for i, e := range files {
		names[i] = e.name
	}
	top, ok := soleFolder(names, "")
	if !ok {
		return "", "", nil
	}
	if !strings.EqualFold(top, "Mods") {
		return top + "/", top, nil
	}
	sub, ok := soleFolder(names, top+"/")
	if !ok {
		return "", "", errors.New("the zip holds several mods; upload them one at a time")
	}
	return top + "/" + sub + "/", sub, nil
}

// ZipMaps lists the maps a mod's zip adds, from the reskate-levels.json that
// installing it would put in the mod's folder, without unpacking the rest.
// A zip without one adds none. An error means installing it would fail too,
// or its reskate-levels.json is one the server skips.
func ZipMaps(zr *zip.Reader) ([]ModMap, error) {
	files, err := modFiles(zr)
	if err != nil {
		return nil, err
	}
	prefix, _, err := modRoot(files)
	if err != nil {
		return nil, err
	}
	for _, e := range files {
		if e.name != prefix+"reskate-levels.json" {
			continue
		}
		rc, err := e.f.Open()
		if err != nil {
			return nil, fmt.Errorf("reskate-levels.json: %w", err)
		}
		defer rc.Close()
		data, err := io.ReadAll(io.LimitReader(rc, 1<<20+1))
		if err != nil {
			return nil, fmt.Errorf("reskate-levels.json: %w", err)
		}
		if len(data) > 1<<20 {
			return nil, errors.New("reskate-levels.json is over 1 MB")
		}
		return parseLevels(data)
	}
	return []ModMap{}, nil
}

// ErrNoMod is a mod folder that is in neither Mods nor DisabledMods.
var ErrNoMod = errors.New("no such mod")

// findMod reports where the mod in folder is: a folder, or a link to one.
func findMod(dir, folder string) (path string, disabled bool, err error) {
	if !ValidModFolder(folder) {
		return "", false, ErrNoMod
	}
	for _, disabled := range []bool{false, true} {
		parent := ModsDir(dir)
		if disabled {
			parent = DisabledModsDir(dir)
		}
		p := filepath.Join(parent, folder)
		if fi, err := os.Lstat(p); err == nil && (fi.IsDir() || linkTarget(p) != "") {
			return p, disabled, nil
		}
	}
	return "", false, ErrNoMod
}

// SetModEnabled moves a mod between Mods and DisabledMods. The server reads
// Mods when it starts, so a running one sees the change after a restart.
func SetModEnabled(dir, folder string, enabled bool) error {
	from, disabled, err := findMod(dir, folder)
	if err != nil {
		return err
	}
	if disabled != enabled {
		return nil
	}
	parent := DisabledModsDir(dir)
	if enabled {
		parent = ModsDir(dir)
	}
	to := filepath.Join(parent, folder)
	if _, err := os.Lstat(to); err == nil {
		return fmt.Errorf("%s already has a mod named %q", filepath.Base(parent), folder)
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	if err := os.Rename(from, to); err != nil {
		return fmt.Errorf("cannot move %s (a running server may be using it): %w", folder, err)
	}
	return nil
}

// DeleteMod removes a mod, enabled or not. It is moved out of the way first so
// the server never finds half a mod. A link goes, not what it leads to.
func DeleteMod(dir, folder string) error {
	from, _, err := findMod(dir, folder)
	if err != nil {
		return err
	}
	if linkTarget(from) != "" {
		return os.Remove(from)
	}
	tmp, err := os.MkdirTemp(dir, ".mod-delete-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := os.Rename(from, filepath.Join(tmp, folder)); err != nil {
		return fmt.Errorf("cannot delete %s (a running server may be using it): %w", folder, err)
	}
	return nil
}

// ZipEntryPath cleans a zip entry's name to a forward-slash path inside the
// zip. Absolute paths, drive letters, ".." and, on Windows, device names such
// as NUL or COM1 are refused.
func ZipEntryPath(name string) (string, bool) {
	name = strings.ReplaceAll(name, `\`, "/")
	if strings.HasPrefix(name, "/") || strings.Contains(name, ":") {
		return "", false
	}
	name = path.Clean(name)
	if name == "." || name == ".." || strings.HasPrefix(name, "../") || !filepath.IsLocal(filepath.FromSlash(name)) {
		return "", false
	}
	return name, true
}

// soleFolder reports the one folder that every name under prefix sits in, if
// there is one and no file sits directly under prefix.
func soleFolder(names []string, prefix string) (string, bool) {
	top := ""
	for _, n := range names {
		first, _, nested := strings.Cut(strings.TrimPrefix(n, prefix), "/")
		if !nested || (top != "" && first != top) {
			return "", false
		}
		top = first
	}
	return top, top != ""
}

// ValidModFolder accepts names that work as a folder on Windows and Linux.
func ValidModFolder(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 128 || strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return false
	}
	for _, r := range name {
		if r < 0x20 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return false
		}
	}
	return true
}

func unzipFile(f *zip.File, to string, budget int64) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return 0, err
	}
	rc, err := f.Open()
	if err != nil {
		return 0, fmt.Errorf("%s: %w", f.Name, err)
	}
	defer rc.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, io.LimitReader(rc, budget+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 0, fmt.Errorf("%s: %w", f.Name, err)
	}
	if n > budget {
		return 0, fmt.Errorf("the mod unpacks to more than %d GB", maxModBytes>>30)
	}
	return budget - n, nil
}

// SweepLeftovers deletes what an install, an upload, a download or a delete
// left beside dir/Mods when the manager stopped part way, and names the old
// copies of mods it leaves: one set aside while its new copy went in is the
// only copy there is. Only for when nothing is installing.
func SweepLeftovers(dir string) (kept []string) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, ".mod-") {
			continue
		}
		if strings.HasSuffix(name, ".old") {
			kept = append(kept, filepath.Join(dir, name))
			continue
		}
		os.RemoveAll(filepath.Join(dir, name))
	}
	return kept
}
