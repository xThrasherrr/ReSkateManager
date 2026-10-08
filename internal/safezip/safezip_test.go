package safezip

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func makeZip(t *testing.T, files int) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := range files {
		w, err := zw.Create(fmt.Sprintf("f%d", i))
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(w, "file %d", i)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestListLimit(t *testing.T) {
	data := makeZip(t, 5000)
	if _, err := NewReader(bytes.NewReader(data), int64(len(data)), 64<<10); !errors.Is(err, ErrTooMany) {
		t.Fatalf("5000 files in 64 KB of names: %v", err)
	}
	zr, err := NewReader(bytes.NewReader(data), int64(len(data)), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(zr.File) != 5000 {
		t.Fatalf("%d files", len(zr.File))
	}
	// Past the list, files read without the limit.
	rc, err := zr.File[4999].Open()
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || string(b) != "file 4999" {
		t.Fatalf("read %q, %v", b, err)
	}
}

func TestOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.zip")
	if err := os.WriteFile(path, makeZip(t, 3), 0o600); err != nil {
		t.Fatal(err)
	}
	zr, err := Open(path, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	if len(zr.File) != 3 {
		t.Fatalf("%d files", len(zr.File))
	}
	if _, err := Open(filepath.Join(t.TempDir(), "missing.zip"), 1<<20); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}
	if err := os.WriteFile(path, []byte("not a zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, 1<<20); err == nil {
		t.Fatal("opened a file that isn't a zip")
	}
}
