//go:build windows

package host

import (
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procGetSystemTimes       = kernel32.NewProc("GetSystemTimes")
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
)

// memoryStatusEx is MEMORYSTATUSEX.
type memoryStatusEx struct {
	length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

func read() (Reading, error) {
	var idle, kernel, user windows.Filetime
	if ok, _, err := procGetSystemTimes.Call(uintptr(unsafe.Pointer(&idle)), uintptr(unsafe.Pointer(&kernel)), uintptr(unsafe.Pointer(&user))); ok == 0 {
		return Reading{}, err
	}
	m := memoryStatusEx{}
	m.length = uint32(unsafe.Sizeof(m))
	if ok, _, err := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&m))); ok == 0 {
		return Reading{}, err
	}
	total := ticks(kernel) + ticks(user) // kernel time includes the idle time
	return Reading{
		Busy:         total - ticks(idle),
		Total:        total,
		MemTotal:     m.TotalPhys,
		MemAvailable: m.AvailPhys,
		MemFree:      m.AvailPhys, // the standby cache isn't broken out
	}, nil
}

// ticks converts a FILETIME duration, counted in 100 ns steps.
func ticks(ft windows.Filetime) time.Duration {
	return time.Duration(int64(ft.HighDateTime)<<32|int64(ft.LowDateTime)) * 100
}
