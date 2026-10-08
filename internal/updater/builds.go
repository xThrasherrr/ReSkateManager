package updater

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"time"
)

// BuildStore keeps the server builds a manager has learned, by SHA-256.
type BuildStore interface {
	ServerBuilds(ctx context.Context) (map[string]string, error)
	AddServerBuild(ctx context.Context, sha256, version string) error
}

// Builds tells which release a server's program is from by its SHA-256, the
// way the server knows itself, from the releases the manager has looked up or
// installed. A nil Builds knows none.
type Builds struct {
	Store BuildStore // keeps what it learns across restarts; nil: until the manager stops
	Log   *slog.Logger

	mu      sync.Mutex
	learned map[string]string // SHA-256 → version
	sums    map[string]fileSum
}

// fileSum is a program's SHA-256 while it keeps its size and time.
type fileSum struct {
	size int64
	mod  time.Time
	sum  string
}

// Load reads the builds Store learned before.
func (b *Builds) Load(ctx context.Context) error {
	if b == nil || b.Store == nil {
		return nil
	}
	m, err := b.Store.ServerBuilds(ctx)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.learned == nil {
		b.learned = map[string]string{}
	}
	for sum, v := range m {
		b.learned[sum] = v
	}
	return nil
}

// Learn records the program of a release, as launcher.json describes it.
func (b *Builds) Learn(rel *Release) {
	if b == nil || rel == nil || !sha256Re.MatchString(rel.ExeSHA256) || rel.Version == "" || len(rel.Version) > 64 {
		return
	}
	if b.Named(rel.ExeSHA256) == rel.Version {
		return
	}
	b.mu.Lock()
	if b.learned == nil {
		b.learned = map[string]string{}
	}
	b.learned[rel.ExeSHA256] = rel.Version
	b.mu.Unlock()
	if b.Store != nil {
		if err := b.Store.AddServerBuild(context.Background(), rel.ExeSHA256, rel.Version); err != nil && b.Log != nil {
			b.Log.Warn("remember a server build", "version", rel.Version, "err", err)
		}
	}
}

// Named is the version of the release whose program has this SHA-256, or ""
// for one the manager hasn't met, such as a build made from source.
func (b *Builds) Named(sum string) string {
	if b == nil || sum == "" {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.learned[sum]
}

// Version is the release the program at exe is from: "" when it isn't there
// or isn't a release this manager knows. The file is hashed again only once
// it changes.
func (b *Builds) Version(exe string) string {
	if b == nil {
		return ""
	}
	fi, err := os.Stat(exe)
	if err != nil {
		return ""
	}
	b.mu.Lock()
	c, ok := b.sums[exe]
	b.mu.Unlock()
	if !ok || c.size != fi.Size() || !c.mod.Equal(fi.ModTime()) {
		sum, err := FileSHA256(exe)
		if err != nil {
			return ""
		}
		c = fileSum{size: fi.Size(), mod: fi.ModTime(), sum: sum}
		b.mu.Lock()
		if b.sums == nil {
			b.sums = map[string]fileSum{}
		}
		b.sums[exe] = c
		b.mu.Unlock()
	}
	return b.Named(c.sum)
}
