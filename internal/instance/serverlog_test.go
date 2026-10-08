package instance

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/xThrasherrr/ReSkateManager/internal/logfile"
	"github.com/xThrasherrr/ReSkateManager/internal/logparse"
)

func TestConsoleStartsWithTheServerLog(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ServerLog), []byte("[2026-10-04 00:54:57] Join code: CODE-1\n[2026-10-04 00:55:50] Shutting down.\n"), 0o644)
	in := New(Def{ID: "x", Dir: dir}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	c := in.Console()
	if len(c) != 3 || c[0].Kind != logparse.KindJoinCode || c[1].Text != "Shutting down." || c[2].Kind != logparse.KindManager {
		t.Fatalf("console %+v", c)
	}
	if empty := New(Def{ID: "y", Dir: t.TempDir()}, slog.New(slog.NewTextHandler(io.Discard, nil))); len(empty.Console()) != 0 {
		t.Fatalf("no log, console %+v", empty.Console())
	}
}

func TestRotateLog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ServerLog)
	os.WriteFile(path, []byte("small"), 0o644)
	if msg, err := rotateLog(dir); msg != "" || err != nil {
		t.Fatalf("small log rotated: %q %v", msg, err)
	}
	os.WriteFile(path, []byte(strings.Repeat("x", logfile.RotateAt)), 0o644)
	if msg, err := rotateLog(dir); msg != "Moved ReSkateServer.log (10 MB) to ReSkateServer.log.1." || err != nil {
		t.Fatalf("big log: %q %v", msg, err)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Error(err)
	}
}

// console.log keeps what the console shows while the server runs, Steam's
// lines too, and not the history read from ReSkateServer.log.
func TestConsoleLog(t *testing.T) {
	d := fakeServer(t)
	os.WriteFile(filepath.Join(d.Dir, ServerLog), []byte("[2026-10-04 00:54:57] Old line from before\n"), 0o644)
	in := New(d, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := in.Start(); err != nil {
		t.Fatal(err)
	}
	waitState(t, in, Running)
	for _, c := range []string{"steam", "join 76561198000000001 Alice"} {
		if _, err := in.Command(context.Background(), c, "owner"); err != nil {
			t.Fatal(err)
		}
	}
	if err := in.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(d.Dir, ConsoleLog)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []logparse.Kind
	var texts []string
	logparse.Scan(strings.NewReader(string(data)), func(e logparse.Entry) bool {
		logparse.ClassifyConsole(&e)
		kinds = append(kinds, e.Kind)
		texts = append(texts, e.Text)
		return true
	})
	all := strings.Join(texts, "\n")
	for _, want := range []string{"Starting ", "Fake is up on San Vansterdam", "relay ping assert", "Alice joined", "Shutting down.", "Server exited with code 0."} {
		if !strings.Contains(all, want) {
			t.Errorf("console.log lacks %q:\n%s", want, data)
		}
	}
	if strings.Contains(all, "Old line from before") {
		t.Error("console.log holds the history read at start")
	}
	if !slices.Contains(kinds, logparse.KindInput) || !slices.Contains(kinds, logparse.KindManager) || !slices.Contains(kinds, logparse.KindJoin) {
		t.Errorf("kinds read back: %v", kinds)
	}
	// Closed once the server stopped: it can be moved, even on Windows.
	if err := os.Rename(path, path+".moved"); err != nil {
		t.Errorf("console.log still open: %v", err)
	}
}

// console.log rotates as it goes, not only before a start.
func TestConsoleLogRotates(t *testing.T) {
	d := fakeServer(t)
	path := filepath.Join(d.Dir, ConsoleLog)
	os.WriteFile(path, []byte(strings.Repeat("x", logfile.RotateAt-100)), 0o644)
	in := New(d, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := in.Start(); err != nil {
		t.Fatal(err)
	}
	waitState(t, in, Running)
	if err := in.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path + ".1"); err != nil || fi.Size() < logfile.RotateAt {
		t.Fatalf("not rotated: %v", err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Size() > 100_000 {
		t.Errorf("the new console.log: %v %v", fi, err)
	}
	if files := LogFiles(d.Dir, ConsoleLog); len(files) != 2 || files[0].Name != ConsoleLog+".1" || files[1].Name != ConsoleLog {
		t.Errorf("LogFiles: %+v", files)
	}
}
