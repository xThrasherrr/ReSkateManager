//go:build windows

package serverconfig

import (
	"encoding/binary"
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// makeLink makes link a directory junction to target, an absolute path. A
// junction, unlike a symlink, needs no admin rights or developer mode.
func makeLink(target, link string) error {
	if err := os.Mkdir(link, 0o755); err != nil {
		return err
	}
	if err := setJunction(link, target); err != nil {
		os.Remove(link)
		return &os.LinkError{Op: "junction", Old: target, New: link, Err: err}
	}
	return nil
}

// setJunction turns the empty folder dir into a junction to target.
func setJunction(dir, target string) error {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)

	// Both names end in NUL; their lengths leave it out.
	sub, err := windows.UTF16FromString(`\??\` + target)
	if err != nil {
		return err
	}
	shown, err := windows.UTF16FromString(target)
	if err != nil {
		return err
	}
	// A REPARSE_DATA_BUFFER holding a MountPointReparseBuffer: the tag, the
	// data length and a reserved word, then the offset and length of each
	// name, then the names.
	names := 2 * (len(sub) + len(shown))
	buf := make([]byte, 16+names)
	le := binary.LittleEndian
	le.PutUint32(buf[0:], windows.IO_REPARSE_TAG_MOUNT_POINT)
	le.PutUint16(buf[4:], uint16(8+names))
	le.PutUint16(buf[8:], 0)
	le.PutUint16(buf[10:], uint16(2*(len(sub)-1)))
	le.PutUint16(buf[12:], uint16(2*len(sub)))
	le.PutUint16(buf[14:], uint16(2*(len(shown)-1)))
	off := 16
	for _, c := range append(sub, shown...) {
		le.PutUint16(buf[off:], c)
		off += 2
	}
	var n uint32
	return windows.DeviceIoControl(h, windows.FSCTL_SET_REPARSE_POINT, &buf[0], uint32(len(buf)), nil, 0, &n, nil)
}

// crossDevice reports whether a rename failed for crossing drives.
func crossDevice(err error) bool { return errors.Is(err, windows.ERROR_NOT_SAME_DEVICE) }
