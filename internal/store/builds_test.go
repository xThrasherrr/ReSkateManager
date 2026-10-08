package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestServerBuilds(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.AddServerBuild(ctx, "aa", "1.2.0"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddServerBuild(ctx, "bb", "1.2.1"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddServerBuild(ctx, "aa", "1.2.0-fixed"); err != nil { // the same program again
		t.Fatal(err)
	}
	got, err := s.ServerBuilds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["aa"] != "1.2.0-fixed" || got["bb"] != "1.2.1" {
		t.Errorf("ServerBuilds = %v", got)
	}
}
