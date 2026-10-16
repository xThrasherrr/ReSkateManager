//go:build unix

package luavm

import "golang.org/x/sys/unix"

func reserve(n uint64) ([]byte, error) {
	return unix.Mmap(-1, 0, int(n), unix.PROT_NONE, unix.MAP_PRIVATE|unix.MAP_ANON|unix.MAP_NORESERVE)
}

// commit makes base[from:to] usable. Wasm grows by 64 KiB pages, a multiple
// of the system's.
func commit(base []byte, from, to uint64) error {
	return unix.Mprotect(base[from:to], unix.PROT_READ|unix.PROT_WRITE)
}

func release(base []byte) { unix.Munmap(base) }
