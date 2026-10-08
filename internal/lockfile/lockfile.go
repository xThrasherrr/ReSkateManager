// Package lockfile keeps two managers off one folder. Each holds a lock on
// data/manager.lock while it runs; the operating system lets it go when the
// process ends, however it ends, so a crash leaves nothing to clean up.
package lockfile

import (
	"errors"
	"os"
)

// ErrLocked is a lock another process holds.
var ErrLocked = errors.New("another process holds the lock")

// Lock is a held lock.
type Lock struct{ f *os.File }

// Acquire takes the lock at path, or returns ErrLocked when another process
// has it. The file stays behind when released; only the lock matters.
func Acquire(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lock(f); err != nil {
		f.Close()
		return nil, err
	}
	return &Lock{f: f}, nil
}

// Release lets the lock go.
func (l *Lock) Release() error { return l.f.Close() }
