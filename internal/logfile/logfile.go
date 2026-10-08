// Package logfile writes logs that keep themselves small: once a log reaches
// RotateAt it moves aside to .1, older copies move one further, and the one
// past Keep goes.
package logfile

import (
	"fmt"
	"os"
	"sync"
)

const (
	RotateAt = 10 << 20 // bytes
	// Keep is how many old copies of a log are kept, as .1 (the newest) to .3.
	Keep = 3
)

// Rotate moves the log at path to path.1, and older copies one further, once
// it has reached RotateAt; the oldest past Keep goes. It reports the size of
// the log it moved, or 0 when it left it.
func Rotate(path string) (int64, error) {
	st, err := os.Stat(path)
	if err != nil || st.Size() < RotateAt {
		return 0, nil
	}
	os.Remove(fmt.Sprintf("%s.%d", path, Keep))
	for i := Keep - 1; i >= 1; i-- {
		os.Rename(fmt.Sprintf("%s.%d", path, i), fmt.Sprintf("%s.%d", path, i+1))
	}
	if err := os.Rename(path, path+".1"); err != nil {
		return 0, err
	}
	return st.Size(), nil
}

// Writer appends to a log, rotating it when it opens and again whenever it
// reaches RotateAt, so one long run can't fill the disk. It is safe for
// concurrent use.
type Writer struct {
	mu   sync.Mutex
	path string
	f    *os.File
	size int64
}

// Open rotates the log at path if it is due, then opens it for appending.
func Open(path string) (*Writer, error) {
	if _, err := Rotate(path); err != nil {
		return nil, err
	}
	w := &Writer{path: path}
	return w, w.open()
}

func (w *Writer) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	w.f, w.size = f, st.Size()
	return nil
}

// Write appends p, then rotates the file once it has grown to RotateAt.
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return 0, os.ErrClosed
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	if err != nil || w.size < RotateAt {
		return n, err
	}
	w.f.Close()
	w.f = nil
	_, rerr := Rotate(w.path)
	if err := w.open(); err != nil {
		return n, err
	}
	if rerr != nil {
		// Something holds the log open (an editor on Windows), so it stays;
		// try again after another RotateAt rather than on every write.
		w.size = 0
	}
	return n, nil
}

// Close closes the log; writes after it fail.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}
