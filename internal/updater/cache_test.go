package updater

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestPruneCache(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	for name, age := range map[string]time.Duration{
		"ReSkateServer-aaaa.zip":       time.Hour,
		"ReSkateServer-bbbb.tar.gz":    2 * time.Hour,
		"ReSkateServer-cccc.zip":       3 * time.Hour,
		"ReSkateServer-dddd.zip.part":  48 * time.Hour,
		"ReSkateServer-eeee.zip.part":  time.Minute, // a download under way
		"something-else.zip":           100 * time.Hour,
		"ReSkateServer-ffff.zip.extra": 100 * time.Hour,
	} {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte("x"), 0o644)
		os.Chtimes(p, now.Add(-age), now.Add(-age))
	}
	gone, err := PruneCache(dir, 2)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(gone)
	if want := []string{"ReSkateServer-cccc.zip", "ReSkateServer-dddd.zip.part"}; !slices.Equal(gone, want) {
		t.Errorf("deleted %v, want %v", gone, want)
	}
	if gone, err := PruneCache(filepath.Join(dir, "missing"), 2); err != nil || len(gone) != 0 {
		t.Errorf("a missing folder: %v, %v", gone, err)
	}
}
