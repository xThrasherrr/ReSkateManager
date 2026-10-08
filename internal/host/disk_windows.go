package host

import (
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

func diskSpace(path string) (Space, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return Space{}, err
	}
	var avail, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &avail, &total, &free); err != nil {
		return Space{}, &os.PathError{Op: "GetDiskFreeSpaceEx", Path: path, Err: err}
	}
	s := Space{Volume: path, Total: total, Used: total - free, Free: avail}
	vol := make([]uint16, windows.MAX_PATH+1)
	if windows.GetVolumePathName(p, &vol[0], uint32(len(vol))) == nil {
		s.Volume = windows.UTF16ToString(vol)
	}
	s.ID = strings.ToUpper(s.Volume)
	return s, nil
}
