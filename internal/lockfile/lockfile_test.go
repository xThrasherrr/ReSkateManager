package lockfile

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestAcquire(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manager.lock")
	l, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("a second lock: %v", err)
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	again, err := Acquire(path)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	again.Release()
}
