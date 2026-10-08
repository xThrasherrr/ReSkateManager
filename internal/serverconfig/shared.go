package serverconfig

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Shared mods are one copy of a mod that several servers load. The shared
// folder is laid out like a server's, with the mods in its Mods folder, and a
// server that uses it gets a link to each shared mod in its own Mods: a
// directory junction on Windows, which needs no admin rights, and a symlink
// elsewhere. The server follows the link like any mod folder.
//
// A link is taken for a shared one when its target ends in
// shared/Mods/<the link's name>, so links are still known after the
// manager's folder moves, and SyncShared points them at the new place.

// SharedName is the folder, under the manager's root, that holds shared mods.
const SharedName = "shared"

// SharedUse is how a server uses the shared mods. It is stored as a number.
type SharedUse int

const (
	SharedOff  SharedUse = iota // not at all
	SharedAll                   // all of them; a mod shared later loads at once
	SharedPick                  // the ones enabled on it; a mod shared later arrives disabled
)

var sharedUses = []string{"off", "all", "pick"}

// MarshalText writes a SharedUse as its JSON name.
func (u SharedUse) MarshalText() ([]byte, error) {
	if u < 0 || int(u) >= len(sharedUses) {
		return nil, fmt.Errorf("bad shared mods use %d", int(u))
	}
	return []byte(sharedUses[u]), nil
}

// UnmarshalText reads a SharedUse from its JSON name.
func (u *SharedUse) UnmarshalText(b []byte) error {
	for i, s := range sharedUses {
		if string(b) == s {
			*u = SharedUse(i)
			return nil
		}
	}
	return fmt.Errorf("shared mods use must be one of %s", strings.Join(sharedUses, ", "))
}

// SyncShared brings server folder dir's links to the shared mods in line
// with the shared folder and use. Each shared mod not linked yet is linked
// into Mods, or with SharedPick into DisabledMods, unless the server has
// something of that name in either: its own copy wins, and a link keeps the
// place it was moved to. Links to mods that are no longer shared go, and with
// SharedOff, all of them do.
//
// A link another sync made or removed meanwhile is no error, so syncs of the
// same server may overlap; the next one fixes whatever a race leaves.
func SyncShared(dir, shared string, use SharedUse) error {
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil // a server deleted meanwhile; its folder must not come back
	}
	lib := ModsDir(shared)
	want := map[string]string{} // by fold, as the shared folder spells it
	if use != SharedOff {
		entries, err := os.ReadDir(lib)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		for _, e := range entries {
			if e.IsDir() && ValidModFolder(e.Name()) {
				want[fold(e.Name())] = e.Name()
			}
		}
	}
	var errs []error
	taken := map[string]bool{}
	for _, parent := range []string{ModsDir(dir), DisabledModsDir(dir)} {
		entries, err := os.ReadDir(parent)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		for _, e := range entries {
			p := filepath.Join(parent, e.Name())
			if !isSharedLink(p) {
				taken[fold(e.Name())] = true
				continue
			}
			name, ok := want[fold(e.Name())]
			if !ok {
				errs = append(errs, unlink(p))
				continue
			}
			taken[fold(e.Name())] = true
			if target := filepath.Join(lib, name); !sameFile(p, target) {
				if err := unlink(p); err != nil {
					errs = append(errs, err)
					continue
				}
				errs = append(errs, link(target, p))
			}
		}
	}
	arrive := ModsDir(dir)
	if use == SharedPick {
		arrive = DisabledModsDir(dir)
	}
	for key, name := range want {
		if taken[key] {
			continue
		}
		if err := os.MkdirAll(arrive, 0o755); err != nil {
			return errors.Join(append(errs, err)...)
		}
		errs = append(errs, link(filepath.Join(lib, name), filepath.Join(arrive, name)))
	}
	return errors.Join(errs...)
}

// link and unlink make and remove a link, taking one that is already there,
// or already gone, for done.
func link(target, path string) error {
	if err := makeLink(target, path); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	return nil
}

func unlink(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// IsShared reports whether the mod in folder comes from the shared mods.
func IsShared(dir, folder string) bool {
	p, _, err := findMod(dir, folder)
	return err == nil && isSharedLink(p)
}

// ShareMod moves a server's own mod into the shared folder and links it back
// in its place, so it stays enabled or disabled. When the shared folder has
// the mod already, the server's copy is swapped for a link to it, but only if
// the two are the same version.
func ShareMod(dir, folder, shared string) error {
	from, _, err := findMod(dir, folder)
	if err != nil {
		return err
	}
	if linkTarget(from) != "" {
		return fmt.Errorf("%s is a link, not the server's own copy", folder)
	}
	to := filepath.Join(ModsDir(shared), folder)
	if _, err := os.Lstat(to); err == nil {
		swapped, err := UseShared(dir, folder, shared)
		if err == nil && !swapped {
			err = fmt.Errorf("the shared mods hold a different version of %s", folder)
		}
		return err
	}
	if err := os.MkdirAll(ModsDir(shared), 0o755); err != nil {
		return err
	}
	if err := os.Rename(from, to); err != nil {
		if crossDevice(err) {
			return fmt.Errorf("%s is on another drive than the shared mods; upload it to the shared mods instead", folder)
		}
		return fmt.Errorf("cannot move %s (a running server may be using it): %w", folder, err)
	}
	if err := makeLink(to, from); err != nil {
		return fmt.Errorf("moved %s to the shared mods, but could not link it back: %w", folder, err)
	}
	return nil
}

// UseShared swaps the server's own copy of a shared mod for a link to the
// shared one, in the same place, if the two are the same version. It reports
// whether it did.
func UseShared(dir, folder, shared string) (bool, error) {
	from, _, err := findMod(dir, folder)
	if errors.Is(err, ErrNoMod) || (err == nil && linkTarget(from) != "") {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	lib := filepath.Join(ModsDir(shared), folder)
	if fi, err := os.Lstat(lib); err != nil || !fi.IsDir() || !sameMod(from, lib) {
		return false, nil
	}
	tmp, err := os.MkdirTemp(dir, ".mod-delete-")
	if err != nil {
		return false, err
	}
	aside := filepath.Join(tmp, folder)
	if err := os.Rename(from, aside); err != nil {
		os.Remove(tmp)
		return false, fmt.Errorf("cannot replace %s (a running server may be using it): %w", folder, err)
	}
	if err := makeLink(lib, from); err != nil {
		// Put the copy back; if even that fails, keep it where it is.
		if rerr := os.Rename(aside, from); rerr != nil {
			return false, fmt.Errorf("could not link %s, and its copy is left in %s: %w", folder, tmp, errors.Join(err, rerr))
		}
		os.Remove(tmp)
		return false, err
	}
	return true, os.RemoveAll(tmp)
}

// RenameShared moves server folder dir's link to shared mod old over to new,
// where an update put it, keeping it enabled or disabled.
func RenameShared(dir, old, new, shared string) error {
	for _, parent := range []string{ModsDir(dir), DisabledModsDir(dir)} {
		p := filepath.Join(parent, old)
		if !isSharedLink(p) {
			continue
		}
		if err := os.Remove(p); err != nil {
			return err
		}
		to := filepath.Join(parent, new)
		if _, err := os.Lstat(to); err == nil {
			return nil // the server has its own; SyncShared leaves it be
		}
		return makeLink(filepath.Join(ModsDir(shared), new), to)
	}
	return nil
}

// linkTarget returns where the link at path leads, or "" when path is not a
// symlink or junction.
func linkTarget(path string) string {
	fi, err := os.Lstat(path)
	// Go reports a junction as irregular rather than as a symlink.
	if err != nil || fi.Mode()&(fs.ModeSymlink|fs.ModeIrregular) == 0 {
		return ""
	}
	t, err := os.Readlink(path)
	if err != nil {
		return ""
	}
	return t
}

// isSharedLink reports whether path is a link to the shared mod of its name.
func isSharedLink(path string) bool {
	t := linkTarget(path)
	if t == "" {
		return false
	}
	t = filepath.Clean(t)
	mods := filepath.Dir(t)
	return fold(filepath.Base(t)) == fold(filepath.Base(path)) &&
		fold(filepath.Base(mods)) == fold("Mods") &&
		fold(filepath.Base(filepath.Dir(mods))) == fold(SharedName)
}

// fold makes names that the file system takes for the same one equal.
func fold(name string) string {
	if runtime.GOOS == "windows" {
		return strings.ToLower(name)
	}
	return name
}

func sameFile(a, b string) bool {
	x, err := os.Stat(a)
	if err != nil {
		return false
	}
	y, err := os.Stat(b)
	return err == nil && os.SameFile(x, y)
}

// sameMod reports whether two mod folders hold the same version of a mod:
// the same title, author and version by their manifests or, when neither
// says a version, the same files at the same sizes.
func sameMod(a, b string) bool {
	var x, y Mod
	readModInfo(a, &x)
	readModInfo(b, &y)
	if x.Version != "" || y.Version != "" {
		return x.Version == y.Version && x.Title == y.Title && x.Author == y.Author
	}
	fa, err := fileSizes(a)
	if err != nil {
		return false
	}
	fb, err := fileSizes(b)
	return err == nil && maps.Equal(fa, fb)
}

// fileSizes maps each regular file under dir to its size. Links are not
// followed.
func fileSizes(dir string) (map[string]int64, error) {
	out := map[string]int64{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		out[fold(filepath.ToSlash(rel))] = fi.Size()
		return nil
	})
	return out, err
}
