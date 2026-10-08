package updater

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// memBuilds is a BuildStore in memory that counts its writes.
type memBuilds struct {
	m      map[string]string
	writes int
}

func (s *memBuilds) ServerBuilds(context.Context) (map[string]string, error) { return s.m, nil }

func (s *memBuilds) AddServerBuild(_ context.Context, sum, v string) error {
	s.m[sum] = v
	s.writes++
	return nil
}

func TestBuildsNamed(t *testing.T) {
	var b Builds
	if b.Named(sum([]byte("never met"))) != "" || b.Named("") != "" {
		t.Error("named a program it never met")
	}
	var none *Builds
	none.Learn(&Release{Version: "1.0.0", ExeSHA256: sum([]byte("x"))})
	if none.Named(sum([]byte("x"))) != "" || none.Version("x") != "" {
		t.Error("a nil Builds knows something")
	}
}

func TestBuildsLearn(t *testing.T) {
	store := &memBuilds{m: map[string]string{}}
	b := &Builds{Store: store}
	sum := sum([]byte("server 9.9.9"))
	for _, bad := range []*Release{nil, {Version: "9.9.9", ExeSHA256: "abc"}, {ExeSHA256: sum}} {
		b.Learn(bad)
	}
	if store.writes != 0 || b.Named(sum) != "" {
		t.Fatalf("learned from a release without a version or SHA-256: %v", store.m)
	}
	b.Learn(&Release{Version: "9.9.9", ExeSHA256: sum})
	if b.Named(sum) != "9.9.9" || store.m[sum] != "9.9.9" {
		t.Fatalf("not learned: %q, store %v", b.Named(sum), store.m)
	}
	// Each lookup of the same release doesn't write it again.
	b.Learn(&Release{Version: "9.9.9", ExeSHA256: sum})
	if store.writes != 1 {
		t.Errorf("%d writes for one release", store.writes)
	}

	// A new manager reads what the last one learned.
	again := &Builds{Store: store}
	if err := again.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if again.Named(sum) != "9.9.9" {
		t.Error("forgot a learned build across Load")
	}
}

func TestBuildsVersion(t *testing.T) {
	b := &Builds{}
	exe := filepath.Join(t.TempDir(), ExeName())
	if b.Version(exe) != "" {
		t.Error("a missing program has a version")
	}
	os.WriteFile(exe, []byte("server 9.9.9"), 0o755)
	b.Learn(&Release{Version: "9.9.9", ExeSHA256: sum([]byte("server 9.9.9"))})
	if v := b.Version(exe); v != "9.9.9" {
		t.Fatalf("Version = %q", v)
	}
	// Replaced by an unknown build: hashed again, since its size changed.
	os.WriteFile(exe, []byte("built from source"), 0o755)
	if v := b.Version(exe); v != "" {
		t.Errorf("Version after the program changed = %q", v)
	}
	// Same size, newer time: hashed again too.
	b.Learn(&Release{Version: "9.9.8", ExeSHA256: sum([]byte("server 9.9.8"))})
	os.WriteFile(exe, []byte("server 9.9.8"), 0o755)
	later := time.Now().Add(time.Minute)
	os.Chtimes(exe, later, later)
	if v := b.Version(exe); v != "9.9.8" {
		t.Errorf("Version after a same-size swap = %q", v)
	}
}

// Every release Latest finds is learned.
func TestCheckerLearns(t *testing.T) {
	exe := sum([]byte("server 9.9.9"))
	manifest := filepath.Join(t.TempDir(), "launcher.json")
	os.WriteFile(manifest, []byte(`{"schema":1,"server":{"version":"9.9.9","url":"https://example.invalid/s.zip","sha256":"`+
		sum([]byte("zip"))+`","size":3,"exe_sha256":"`+exe+`"}}`), 0o644)
	b := &Builds{}
	c := NewChecker("unused")
	c.GOOS, c.LauncherJSON, c.Learn = "windows", manifest, b.Learn
	if _, err := c.Latest(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if b.Named(exe) != "9.9.9" {
		t.Error("Latest's release wasn't learned")
	}
}
