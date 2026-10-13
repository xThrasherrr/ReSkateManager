package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A release built from its tag, or from a hotfix after it, as 2.0.7 was:
// only a later change to Server/ counts, and a pull request's from its merge.
func TestBuiltFrom(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	run := func(date string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date,
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	commit := func(date, file string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(file)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, file), []byte(date), 0o644); err != nil {
			t.Fatal(err)
		}
		run(date, "add", "-A")
		run(date, "commit", "-q", "-m", file)
	}
	head := func() string {
		t.Helper()
		out, err := git(dir, "rev-parse", "HEAD")
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(out)
	}
	run("2026-10-11T00:00:00Z", "init", "-q", "-b", "main")
	commit("2026-10-11T01:00:00Z", "Server/main.cpp")
	run("2026-10-11T01:00:00Z", "tag", "v1")
	// A pull request written now, on the tag, and merged after the hotfix's
	// files were built.
	run("2026-10-11T01:10:00Z", "checkout", "-q", "-b", "pr")
	commit("2026-10-11T03:30:00Z", "Server/pr.cpp")
	run("2026-10-11T03:30:00Z", "checkout", "-q", "main")
	commit("2026-10-11T02:00:00Z", "Engine/game.cpp") // not the server's
	commit("2026-10-11T03:00:00Z", "Server/hotfix.cpp")
	hotfix := head()
	run("2026-10-11T05:00:00Z", "merge", "-q", "--no-ff", "-m", "merge pr", "pr")
	merged := head()
	for at, want := range map[string]string{
		"2026-10-11T01:30:00Z": "v1",
		"2026-10-11T02:30:00Z": "v1", // only the game changed since
		"2026-10-11T04:00:00Z": hotfix,
		"2026-10-11T06:00:00Z": merged,
	} {
		when, _ := time.Parse(time.RFC3339, at)
		if got, err := builtFrom(dir, "v1", when); err != nil || got != want {
			t.Errorf("built by %s: %q (%v), want %q", at, got, err, want)
		}
	}
}

func TestSourceName(t *testing.T) {
	if got := sourceName("v2.0.7", "d2b8e180973b1c997a4329afda3e0cef8300fdb2"); got != "v2.0.7 as built (d2b8e18)" {
		t.Errorf("sourceName = %q", got)
	}
	if got := sourceName("v2.0.6", "v2.0.6"); got != "v2.0.6" {
		t.Errorf("sourceName = %q", got)
	}
}
