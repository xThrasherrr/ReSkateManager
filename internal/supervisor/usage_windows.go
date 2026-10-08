//go:build windows

package supervisor

import (
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procGetProcessMemoryInfo = windows.NewLazySystemDLL("kernel32.dll").NewProc("K32GetProcessMemoryInfo")

// processMemoryCounters is PROCESS_MEMORY_COUNTERS.
type processMemoryCounters struct {
	cb                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

func usage(pid int) (Usage, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return Usage{}, err
	}
	defer windows.CloseHandle(h)
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return Usage{}, err
	}
	mc := processMemoryCounters{}
	mc.cb = uint32(unsafe.Sizeof(mc))
	if ok, _, err := procGetProcessMemoryInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&mc)), uintptr(mc.cb)); ok == 0 {
		return Usage{}, err
	}
	return Usage{CPU: ticks(kernel) + ticks(user), Mem: uint64(mc.WorkingSetSize)}, nil
}

// ticks converts a FILETIME duration, counted in 100 ns steps.
func ticks(ft windows.Filetime) time.Duration {
	return time.Duration(int64(ft.HighDateTime)<<32|int64(ft.LowDateTime)) * 100
}
