package luavm

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

func reserve(n uint64) ([]byte, error) {
	addr, err := windows.VirtualAlloc(0, uintptr(n), windows.MEM_RESERVE, windows.PAGE_NOACCESS)
	if err != nil {
		return nil, err
	}
	return unsafe.Slice(*(**byte)(unsafe.Pointer(&addr)), n), nil
}

// commit makes base[from:to] usable. Wasm grows by 64 KiB pages, a multiple
// of the system's.
func commit(base []byte, from, to uint64) error {
	_, err := windows.VirtualAlloc(uintptr(unsafe.Pointer(&base[from])), uintptr(to-from), windows.MEM_COMMIT, windows.PAGE_READWRITE)
	return err
}

func release(base []byte) {
	windows.VirtualFree(uintptr(unsafe.Pointer(&base[0])), 0, windows.MEM_RELEASE)
}
