package host

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
)

// Space is the filesystem holding a folder: its size, what is in use on it,
// and what is still free.
type Space struct {
	// Volume names it: where it is mounted on Linux ("/", "/data"), its drive
	// or mounted folder on Windows ("C:\").
	Volume string `json:"volume"`
	// ID tells filesystems apart where Volume can't: Docker volumes on one
	// disk are mounted apart but share it, and its space.
	ID    string `json:"-"`
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
	// Free is what the manager can still write. On Linux the blocks kept
	// back for root aren't in it, so Used and Free can add up to less than
	// Total.
	Free uint64 `json:"free"`
}

// DiskSpace reads the filesystem holding path, or that would hold it: for a
// folder not made yet (cache/ before the first download), the one holding the
// nearest folder above it.
func DiskSpace(path string) (Space, error) { return diskSpace(existing(path)) }

// Drives reads the filesystems holding dirs, each once however many of dirs
// are on it, in the order first seen; a dir that is "" or can't be read is
// left out. volume maps each dir read to its drive's Volume, which is the
// name the drive was first seen under.
func Drives(dirs []string) (drives []Space, volume map[string]string) {
	drives, volume = []Space{}, map[string]string{}
	for _, dir := range dirs {
		if _, ok := volume[dir]; ok || dir == "" {
			continue
		}
		s, err := DiskSpace(dir)
		if err != nil {
			continue
		}
		if i := slices.IndexFunc(drives, func(d Space) bool { return d.ID == s.ID }); i >= 0 {
			s = drives[i]
		} else {
			drives = append(drives, s)
		}
		volume[dir] = s.Volume
	}
	return drives, volume
}

// existing is dir, or the nearest folder above it that exists.
func existing(dir string) string {
	for {
		_, err := os.Stat(dir)
		parent := filepath.Dir(dir)
		if !errors.Is(err, fs.ErrNotExist) || parent == dir {
			return dir
		}
		dir = parent
	}
}

// DirSize adds up the sizes of the files under dir. It doesn't follow links
// (junctions on Windows), so a shared mod linked into a server's Mods counts
// once, in the shared folder, and a folder it can't read counts as empty. A
// missing dir is 0.
func DirSize(dir string) (int64, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var n int64
	for _, e := range entries {
		switch {
		case e.IsDir():
			size, _ := DirSize(filepath.Join(dir, e.Name()))
			n += size
		case e.Type().IsRegular():
			if fi, err := e.Info(); err == nil {
				n += fi.Size()
			}
		}
	}
	return n, nil
}
