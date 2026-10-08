package logparse

import (
	"strings"
	"time"
)

// Assembler groups raw stdout lines into entries. A stamped line starts a new
// entry; unstamped lines extend the pending one. The pending entry is complete
// when the next stamped line arrives or when Flush is called after the pipe
// goes quiet (the server prints a whole entry in one write, so a short idle
// gap means it is done).
type Assembler struct {
	pending *Entry
	lines   []string
	now     func() time.Time
}

// NewAssembler makes an assembler that stamps entries with the time now.
func NewAssembler() *Assembler { return &Assembler{now: time.Now} }

// Push adds one line (without its newline) and returns any entry it completed.
func (a *Assembler) Push(line string) *Entry {
	line = strings.TrimRight(line, "\r")
	stamp, text, ok := SplitStamp(line)
	if !ok && a.pending != nil {
		a.lines = append(a.lines, line)
		return nil
	}
	done := a.Flush()
	a.pending = &Entry{Stamp: stamp, At: a.now().UnixMilli()}
	a.lines = append(a.lines[:0], text)
	return done
}

// Flush completes the pending entry, if any.
func (a *Assembler) Flush() *Entry {
	if a.pending == nil {
		return nil
	}
	e := a.pending
	e.Text = strings.Join(a.lines, "\n")
	a.pending, a.lines = nil, a.lines[:0]
	Classify(e)
	return e
}

// Pending reports whether an entry is held, waiting for more of its lines.
func (a *Assembler) Pending() bool { return a.pending != nil }
