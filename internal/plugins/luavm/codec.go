package luavm

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
)

// Values cross between Go and Lua in the encoding wasm/bridge.c describes. In
// Go a Lua value is nil, bool, int64, float64, string, Ref or *Table. Encode
// also takes the other integer types, []any (a list) and map[string]any.

// Ref is a function the plugin handed the host, held in its Lua's registry
// until released.
type Ref uint32

// Table is a Lua table, its pairs in the order Lua gave them.
type Table struct {
	Keys, Vals []any
}

// Get is the value at key: a string, or an int64 for a list's entries.
func (t *Table) Get(key any) any {
	if t == nil {
		return nil
	}
	if i, ok := key.(int); ok {
		key = int64(i)
	}
	for i, k := range t.Keys {
		if k == key {
			return t.Vals[i]
		}
	}
	return nil
}

// List is the table's entries 1, 2, 3... up to the first missing one.
func (t *Table) List() []any {
	var out []any
	for i := int64(1); ; i++ {
		v := t.Get(i)
		if v == nil {
			return out
		}
		out = append(out, v)
	}
}

// maxDepth bounds tables inside tables, as the bridge does.
const maxDepth = 16

var errShort = errors.New("value cut short")

type reader struct {
	b    []byte
	refs []Ref // functions seen, so the ones nobody keeps can be released
}

func (r *reader) byte() (byte, error) {
	if len(r.b) < 1 {
		return 0, errShort
	}
	c := r.b[0]
	r.b = r.b[1:]
	return c, nil
}

func (r *reader) u32() (uint32, error) {
	if len(r.b) < 4 {
		return 0, errShort
	}
	v := binary.LittleEndian.Uint32(r.b)
	r.b = r.b[4:]
	return v, nil
}

func (r *reader) u64() (uint64, error) {
	if len(r.b) < 8 {
		return 0, errShort
	}
	v := binary.LittleEndian.Uint64(r.b)
	r.b = r.b[8:]
	return v, nil
}

func (r *reader) value(depth int) (any, error) {
	kind, err := r.byte()
	if err != nil {
		return nil, err
	}
	switch kind {
	case 'n':
		return nil, nil
	case 'f':
		return false, nil
	case 't':
		return true, nil
	case 'i':
		v, err := r.u64()
		return int64(v), err
	case 'd':
		v, err := r.u64()
		return math.Float64frombits(v), err
	case 's':
		n, err := r.u32()
		if err != nil {
			return nil, err
		}
		if uint64(n) > uint64(len(r.b)) {
			return nil, errShort
		}
		s := string(r.b[:n])
		r.b = r.b[n:]
		return s, nil
	case 'F':
		v, err := r.u32()
		if err != nil {
			return nil, err
		}
		r.refs = append(r.refs, Ref(v))
		return Ref(v), nil
	case 'T':
		if depth >= maxDepth {
			return nil, errors.New("table nested too deeply")
		}
		n, err := r.u32()
		if err != nil {
			return nil, err
		}
		if uint64(n)*2 > uint64(len(r.b)) { // each pair takes 2 bytes at least
			return nil, errShort
		}
		t := &Table{Keys: make([]any, 0, n), Vals: make([]any, 0, n)}
		for range n {
			k, err := r.value(depth + 1)
			if err != nil {
				return nil, err
			}
			v, err := r.value(depth + 1)
			if err != nil {
				return nil, err
			}
			t.Keys, t.Vals = append(t.Keys, k), append(t.Vals, v)
		}
		return t, nil
	}
	return nil, fmt.Errorf("unknown value kind %q", kind)
}

// decodeList reads a list of values, and the functions among them.
func decodeList(b []byte) ([]any, []Ref, error) {
	r := &reader{b: b}
	n, err := r.u32()
	if err != nil {
		return nil, nil, err
	}
	if uint64(n) > uint64(len(r.b)) { // a byte each at least
		return nil, r.refs, errShort
	}
	out := make([]any, 0, n)
	for range n {
		v, err := r.value(0)
		if err != nil {
			return nil, r.refs, err
		}
		out = append(out, v)
	}
	if len(r.b) != 0 {
		return nil, r.refs, errors.New("value has bytes left over")
	}
	return out, r.refs, nil
}

func encodeList(vals []any) ([]byte, error) {
	b := binary.LittleEndian.AppendUint32(make([]byte, 0, 64), uint32(len(vals)))
	var err error
	for _, v := range vals {
		if b, err = encode(b, v, 0); err != nil {
			return nil, err
		}
	}
	return b, nil
}

func encodeInt(b []byte, v int64) []byte {
	return binary.LittleEndian.AppendUint64(append(b, 'i'), uint64(v))
}

func encode(b []byte, v any, depth int) ([]byte, error) {
	if depth > maxDepth {
		return nil, errors.New("table nested too deeply")
	}
	var err error
	switch v := v.(type) {
	case nil:
		return append(b, 'n'), nil
	case bool:
		if v {
			return append(b, 't'), nil
		}
		return append(b, 'f'), nil
	case int:
		return encodeInt(b, int64(v)), nil
	case int32:
		return encodeInt(b, int64(v)), nil
	case int64:
		return encodeInt(b, v), nil
	case uint32:
		return encodeInt(b, int64(v)), nil
	case float64:
		return binary.LittleEndian.AppendUint64(append(b, 'd'), math.Float64bits(v)), nil
	case string:
		b = binary.LittleEndian.AppendUint32(append(b, 's'), uint32(len(v)))
		return append(b, v...), nil
	case Ref:
		return binary.LittleEndian.AppendUint32(append(b, 'F'), uint32(v)), nil
	case []any:
		b = binary.LittleEndian.AppendUint32(append(b, 'T'), uint32(len(v)))
		for i, e := range v {
			b = encodeInt(b, int64(i+1))
			if b, err = encode(b, e, depth+1); err != nil {
				return nil, err
			}
		}
		return b, nil
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		b = binary.LittleEndian.AppendUint32(append(b, 'T'), uint32(len(keys)))
		for _, k := range keys {
			if b, err = encode(b, k, depth+1); err != nil {
				return nil, err
			}
			if b, err = encode(b, v[k], depth+1); err != nil {
				return nil, err
			}
		}
		return b, nil
	case *Table:
		b = binary.LittleEndian.AppendUint32(append(b, 'T'), uint32(len(v.Keys)))
		for i := range v.Keys {
			if b, err = encode(b, v.Keys[i], depth+1); err != nil {
				return nil, err
			}
			if b, err = encode(b, v.Vals[i], depth+1); err != nil {
				return nil, err
			}
		}
		return b, nil
	}
	return nil, fmt.Errorf("a Go %T has no Lua value", v)
}

// Marshal is a value to keep: nil, a boolean, number, string, or a table of
// those. A function can't be kept, since it lives only in the Lua it came from.
func Marshal(v any) ([]byte, error) {
	var funcs func(v any) bool
	funcs = func(v any) bool {
		switch v := v.(type) {
		case Ref:
			return true
		case *Table:
			return slices.ContainsFunc(v.Keys, funcs) || slices.ContainsFunc(v.Vals, funcs)
		}
		return false
	}
	if funcs(v) {
		return nil, errors.New("a function can't be kept")
	}
	return encode(nil, v, 0)
}

// Unmarshal is a value Marshal made.
func Unmarshal(b []byte) (any, error) {
	r := &reader{b: b}
	v, err := r.value(0)
	if err == nil && len(r.b) != 0 {
		err = errors.New("value has bytes left over")
	}
	return v, err
}

// TypeName is what Lua calls a value's type, for messages.
func TypeName(v any) string {
	switch v.(type) {
	case nil:
		return "nil"
	case bool:
		return "boolean"
	case int64, float64:
		return "number"
	case string:
		return "string"
	case Ref:
		return "function"
	case *Table:
		return "table"
	}
	return strings.TrimPrefix(fmt.Sprintf("%T", v), "*")
}
