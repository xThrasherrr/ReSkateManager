package luavm

import (
	"github.com/tetratelabs/wazero/experimental"
)

// vmem is a plugin's memory: address space for all of MemoryLimit reserved
// up front, outside the Go heap, and committed as Lua grows into it. Growing
// never copies it, the garbage collector never counts it, and its pages go
// back to the system as soon as the VM closes.
type vmem struct {
	buf  []byte // the committed part; its capacity is all that's reserved
	base []byte // all of it, to free
}

var _ experimental.LinearMemory = (*vmem)(nil)

func newVMem(max uint64) (*vmem, error) {
	base, err := reserve(max)
	if err != nil {
		return nil, err
	}
	return &vmem{buf: base[:0], base: base}, nil
}

// Reallocate grows the memory to size bytes, or returns nil when it can't,
// which Lua sees as running out of memory.
func (m *vmem) Reallocate(size uint64) []byte {
	if size > uint64(cap(m.buf)) {
		return nil
	}
	if have := uint64(len(m.buf)); size > have {
		if err := commit(m.base, have, size); err != nil {
			return nil
		}
		m.buf = m.buf[:size]
	}
	return m.buf[:size]
}

func (m *vmem) Free() {
	if m.base != nil {
		release(m.base)
		m.base, m.buf = nil, nil
	}
}
