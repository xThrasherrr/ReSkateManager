//go:build !windows

package serverconfig

import (
	"errors"
	"os"
	"syscall"
)

// makeLink makes link a symlink to target, an absolute path.
func makeLink(target, link string) error { return os.Symlink(target, link) }

// crossDevice reports whether a rename failed for crossing file systems.
func crossDevice(err error) bool { return errors.Is(err, syscall.EXDEV) }
