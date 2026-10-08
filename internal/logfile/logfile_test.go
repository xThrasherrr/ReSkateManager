package logfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.log")
	os.WriteFile(path, []byte("small"), 0o644)
	if n, err := Rotate(path); n != 0 || err != nil {
		t.Fatalf("small log rotated: %d %v", n, err)
	}
	big := strings.Repeat("x", RotateAt)
	for i := 1; i <= Keep+1; i++ {
		os.WriteFile(path, []byte(big[:len(big)-1]+string(rune('0'+i))), 0o644)
		if n, err := Rotate(path); n != RotateAt || err != nil {
			t.Fatalf("rotation %d: %d %v", i, n, err)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the live log is still there")
	}
	// Four rotations keep the newest three: .1 is the 4th, .3 the 2nd.
	for n, want := range map[int]byte{1: '4', 2: '3', 3: '2'} {
		data, err := os.ReadFile(path + "." + string(rune('0'+n)))
		if err != nil || data[len(data)-1] != want {
			t.Errorf(".%d: %v, ends %q, want %q", n, err, data[len(data)-1:], want)
		}
	}
	if _, err := os.Stat(path + ".4"); !os.IsNotExist(err) {
		t.Error("kept more than three")
	}
}

// A Writer rotates as it goes, not only when it opens.
func TestWriterRotates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.log")
	os.WriteFile(path, []byte(strings.Repeat("x", RotateAt-100)), 0o644)
	w, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	line := []byte(strings.Repeat("y", 199) + "\n")
	if _, err := w.Write(line); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path + ".1"); err != nil || fi.Size() != RotateAt+100 {
		t.Fatalf("not rotated: %v %v", fi, err)
	}
	if _, err := w.Write(line); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != string(line) {
		t.Errorf("the new log: %q %v", data, err)
	}
	if _, err := w.Write(line); err == nil {
		t.Error("wrote after Close")
	}
	// Opening a full log rotates it first.
	os.WriteFile(path, []byte(strings.Repeat("x", RotateAt)), 0o644)
	if w, err = Open(path); err != nil {
		t.Fatal(err)
	}
	w.Close()
	if fi, err := os.Stat(path); err != nil || fi.Size() != 0 {
		t.Errorf("opened without rotating: %v %v", fi, err)
	}
}
