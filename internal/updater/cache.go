package updater

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// PruneCache keeps the keep server release archives in dir used most recently
// (Download marks one used as it reuses it) and deletes the others, along
// with downloads left unfinished for a day. It reports what it deleted.
func PruneCache(dir string, keep int) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	type archive struct {
		name string
		used time.Time
	}
	var archives []archive
	var gone []string
	var errs []error
	remove := func(name string) {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			errs = append(errs, err)
		} else {
			gone = append(gone, name)
		}
	}
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || !strings.HasPrefix(name, "ReSkateServer-") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		switch {
		case strings.HasSuffix(name, ".part"):
			if time.Since(info.ModTime()) > 24*time.Hour {
				remove(name)
			}
		case strings.HasSuffix(name, ".zip"), strings.HasSuffix(name, ".tar.gz"):
			archives = append(archives, archive{name, info.ModTime()})
		}
	}
	slices.SortFunc(archives, func(a, b archive) int { return b.used.Compare(a.used) })
	for i := keep; i < len(archives); i++ {
		remove(archives[i].name)
	}
	return gone, errors.Join(errs...)
}
