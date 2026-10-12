// Package atomicfile replaces a file whole or not at all.
package atomicfile

import (
	"os"
	"path/filepath"
)

// Write puts data in path with perm by way of a temporary file beside it, so a
// crash mid-write leaves the old file rather than half of the new one.
func Write(path string, data []byte, perm os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name()) // gone already, once renamed
	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(perm)
	}
	if err == nil {
		err = f.Sync() // on disk before it replaces the old file
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
