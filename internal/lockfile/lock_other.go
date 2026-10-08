//go:build !windows

package lockfile

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func lock(f *os.File) error {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return ErrLocked
	}
	return err
}
