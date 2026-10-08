package host

import (
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

func diskSpace(path string) (Space, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return Space{}, &os.PathError{Op: "statfs", Path: path, Err: err}
	}
	unit := uint64(st.Frsize)
	if unit == 0 {
		unit = uint64(st.Bsize)
	}
	vol, dev := mountPoint(path)
	return Space{
		Volume: vol,
		ID:     strconv.FormatUint(dev, 10),
		Total:  st.Blocks * unit,
		Used:   (st.Blocks - st.Bfree) * unit,
		Free:   st.Bavail * unit,
	}, nil
}

// mountPoint is the topmost folder above path on the same filesystem, where
// that filesystem is mounted, and the device number that tells it apart.
func mountPoint(path string) (string, uint64) {
	if p, err := filepath.EvalSymlinks(path); err == nil {
		path = p
	}
	path, _ = filepath.Abs(path)
	dev := func(p string) (uint64, bool) {
		fi, err := os.Stat(p)
		if err != nil {
			return 0, false
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			return 0, false
		}
		return uint64(st.Dev), true
	}
	d, ok := dev(path)
	if !ok {
		return path, 0
	}
	for {
		parent := filepath.Dir(path)
		if parent == path {
			return path, d
		}
		if pd, ok := dev(parent); !ok || pd != d {
			return path, d
		}
		path = parent
	}
}
