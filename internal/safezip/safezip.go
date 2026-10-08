// Package safezip opens zips that may have been made to hurt. archive/zip
// reads a zip's whole list of files into memory before anything can count
// them, at a few hundred bytes per entry, and an entry in that list takes
// only 46 bytes of the zip; so a small upload can list millions of files and
// take the manager's memory. Here the list may take only so many bytes.
package safezip

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"sync/atomic"
)

// ErrTooMany is a zip whose list of files is over the limit it was opened with.
var ErrTooMany = errors.New("the zip lists too many files")

// ReadCloser is an open zip file.
type ReadCloser struct {
	*zip.Reader
	f *os.File
}

// Close closes the zip's file.
func (rc *ReadCloser) Close() error { return rc.f.Close() }

// Open opens the zip at name, reading no more than maxList bytes of it before
// its list of files is in memory: about maxList/50 files at most, in about 5
// times maxList of memory. Like zip.OpenReader, it returns a usable reader
// along with zip.ErrInsecurePath.
func Open(name string, maxList int64) (*ReadCloser, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	zr, err := NewReader(f, fi.Size(), maxList)
	if err != nil && !errors.Is(err, zip.ErrInsecurePath) {
		f.Close()
		return nil, err
	}
	return &ReadCloser{Reader: zr, f: f}, err
}

// NewReader is Open for a zip in r, size bytes long.
func NewReader(r io.ReaderAt, size, maxList int64) (*zip.Reader, error) {
	lr := &limitedReaderAt{r: r}
	lr.left.Store(maxList)
	zr, err := zip.NewReader(lr, size)
	over := lr.over.Load()
	lr.left.Store(-1) // files are read through it later, without a limit
	if over {
		return nil, fmt.Errorf("%w: over %d MB of names", ErrTooMany, maxList>>20)
	}
	return zr, err
}

// limitedReaderAt reads up to left bytes, or without a limit once left is
// negative.
type limitedReaderAt struct {
	r    io.ReaderAt
	left atomic.Int64
	over atomic.Bool
}

func (l *limitedReaderAt) ReadAt(p []byte, off int64) (int, error) {
	left := l.left.Load()
	if left < 0 {
		return l.r.ReadAt(p, off)
	}
	if int64(len(p)) > left {
		p = p[:left]
		l.over.Store(true)
	}
	n, err := l.r.ReadAt(p, off)
	l.left.Add(-int64(n))
	if err == nil && l.over.Load() {
		err = ErrTooMany
	}
	return n, err
}
