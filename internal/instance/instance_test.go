package instance

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/logparse"
	"github.com/xThrasherrr/ReSkateManager/internal/serverconfig"
)

func fakeServer(t *testing.T) Def {
	t.Helper()
	dir := t.TempDir()
	d := Def{ID: "fake", Name: "Fake", Dir: dir}
	out, err := exec.Command("go", "build", "-o", d.Exe(), "./testdata/fakeserver").CombinedOutput()
	if err != nil {
		t.Fatalf("build fake server: %v\n%s", err, out)
	}
	return d
}

func waitState(t *testing.T, in *Instance, want State) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for in.State() != want {
		if time.Now().After(deadline) {
			t.Fatalf("state %s, want %s", in.State(), want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestLifecycleRepliesAndRoster(t *testing.T) {
	in := New(fakeServer(t), slog.New(slog.NewTextHandler(io.Discard, nil)))
	joined := make(chan Player, 4)
	in.OnJoin = func(p Player) { joined <- p }
	if err := in.Start(); err != nil {
		t.Fatal(err)
	}
	waitState(t, in, Running)
	// Sent while the startup lines are still arriving: the reply must be the
	// status line, not "1 admin(s). Type help for commands.".
	reply, err := in.Command(context.Background(), "status", "test")
	if err != nil || !strings.HasPrefix(reply, "Fake | San Vansterdam | 0/16") {
		t.Fatalf("status reply %q, %v", reply, err)
	}
	v := in.View()
	if v.Info.JoinCode != "CODE-1" || v.Info.SteamID != "90000000000000001" || v.Info.Map != "San Vansterdam" {
		t.Errorf("info %+v", v.Info)
	}
	for _, c := range []string{"join 76561198000000001 Alice", "join 76561198000000002 Bob"} {
		if _, err := in.Command(context.Background(), c, "test"); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(in.Players()); got != 2 {
		t.Fatalf("roster %d", got)
	}
	<-joined
	admin := func(cmd string) bool {
		t.Helper()
		if _, err := in.Command(context.Background(), cmd, "test"); err != nil {
			t.Fatal(err)
		}
		for _, p := range in.Players() {
			if p.Name == "Bob" {
				return p.Admin
			}
		}
		t.Fatal("Bob left")
		return false
	}
	if !admin("admin add 76561198000000002") {
		t.Error("Bob is not an admin after admin add")
	}
	if admin("admin remove 76561198000000002") {
		t.Error("Bob is still an admin after admin remove")
	}
	reply, _ = in.Command(context.Background(), "players", "test")
	if !strings.HasPrefix(reply, "2 players\n") {
		t.Errorf("players reply %q", reply)
	}
	in.Command(context.Background(), "leave 76561198000000001", "test")
	if ps := in.Players(); len(ps) != 1 || ps[0].Name != "Bob" {
		t.Errorf("after leave %+v", ps)
	}
	if _, err := in.Command(context.Background(), "quit", "test"); err == nil {
		t.Error("quit passed through")
	}
	// Nor as what a poll-run runs for its winning answer (2.0.3).
	for _, line := range []string{"poll-run update | Update now? | yes | no", "Poll-Run {answer} | What now? | play | QUIT"} {
		if _, err := in.Command(context.Background(), line, "test"); err == nil {
			t.Errorf("%q passed through", line)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := in.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if in.State() != Stopped || len(in.Players()) != 0 {
		t.Errorf("after stop: %s, %d players", in.State(), len(in.Players()))
	}
	var sawShutdown bool
	for _, e := range in.Console() {
		if e.Kind == "shutdown" {
			sawShutdown = true
		}
	}
	if !sawShutdown {
		t.Error("no graceful shutdown line")
	}
}

// From ReSkate 2.0.6 the roster has each player's role, from their join and
// the role command, and the default one everybody else has isn't kept.
func TestRosterRoles(t *testing.T) {
	d := fakeServer(t)
	if err := os.MkdirAll(filepath.Join(d.Dir, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	roles := `{"default_role": "player", "roles": [{"name": "player"}, {"name": "vip"}, {"name": "admin", "commands": ["*"]}],
		"players": {"76561198000000001": "admin"}}`
	if err := os.WriteFile(filepath.Join(d.Dir, serverconfig.RolesFile), []byte(roles), 0o644); err != nil {
		t.Fatal(err)
	}
	in := New(d, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := in.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		in.Stop(ctx)
	})
	waitState(t, in, Running)
	run := func(cmd string) map[string]Player {
		t.Helper()
		if _, err := in.Command(context.Background(), cmd, "test"); err != nil {
			t.Fatal(err)
		}
		out := map[string]Player{}
		for _, p := range in.Players() {
			out[p.Name] = p
		}
		return out
	}
	// Straight after it's up, the server prints what words answers; the
	// reply to a words sent then is still the reply.
	if reply, err := in.Command(context.Background(), "words global off", "test"); err != nil || reply != "Chat words: the ReSkate team's lists are off." {
		t.Errorf("words reply %q, %v", reply, err)
	}
	run("join 76561198000000001 Alice")
	ps := run("join 76561198000000002 Bob")
	if a, b := ps["Alice"], ps["Bob"]; !a.Admin || a.Role != "admin" || b.Admin || b.Role != "" {
		t.Errorf("joined %+v %+v", a, b)
	}
	if b := run("role 76561198000000002 vip")["Bob"]; b.Role != "vip" || b.Admin {
		t.Errorf("after role vip: %+v", b)
	}
	if b := run("role 76561198000000002 admin")["Bob"]; b.Role != "admin" || !b.Admin {
		t.Errorf("after role admin: %+v", b)
	}
	if b := run("role 76561198000000002 none")["Bob"]; b.Role != "" || b.Admin {
		t.Errorf("after role none: %+v", b)
	}
	// The list the roster is checked against names the roles too.
	in.Reconcile()
	if ps := run("status"); len(ps) != 2 || ps["Alice"].Role != "admin" || ps["Bob"].Role != "" || ps["Bob"].Name != "Bob" {
		t.Errorf("after a check %+v", ps)
	}
}

// park-mod's reply (2.0.6) is a [parks] line, which is otherwise the server's
// own: it answers park-mod, and only park-mod.
func TestTaggedReply(t *testing.T) {
	in := New(fakeServer(t), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := in.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		in.Stop(ctx)
	})
	waitState(t, in, Running)
	if reply, err := in.Command(context.Background(), "park-mod config add street | street | San Vansterdam", "test"); err != nil ||
		reply != "[parks] config add street | street | San Vansterdam by console" {
		t.Errorf("park-mod reply %q, %v", reply, err)
	}
	// Logged by itself, a [parks] line is no command's reply.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if reply, err := in.Command(ctx, "parks street", "test"); err == nil {
		t.Errorf("a park's own line taken for a reply: %q", reply)
	}
}

// The server refuses to start with a name saved before its name rule, so the
// manager fixes the name first.
func TestStartFixesServerName(t *testing.T) {
	d := fakeServer(t)
	if err := os.WriteFile(d.ConfigPath(), []byte(`{"name": "Thrasher's Park!", "port": 27015}`), 0o644); err != nil {
		t.Fatal(err)
	}
	in := New(d, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := in.Start(); err != nil {
		t.Fatal(err)
	}
	waitState(t, in, Running)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	defer in.Stop(ctx)
	f, err := serverconfig.Read(d.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	name, _ := f.Get("name")
	port, _ := f.Get("port")
	if name != "Thrashers Park" || fmt.Sprint(port) != "27015" {
		t.Errorf("name %v, port %v", name, port)
	}
	if !slices.ContainsFunc(in.Console(), func(e logparse.Entry) bool { return strings.HasPrefix(e.Text, "Renamed the server") }) {
		t.Error("the rename is not in the console")
	}
}

// A good name, no name and no file are left alone.
func TestFixServerNameLeavesOthers(t *testing.T) {
	dir := t.TempDir()
	for _, body := range []string{`{"name": "[EU] Park (24x7)"}`, `{"port": 27015}`, `{"name": 5}`, `not json`} {
		path := filepath.Join(dir, "ReSkateServer.json")
		os.WriteFile(path, []byte(body), 0o644)
		if msg, err := fixServerName(path); msg != "" || err != nil {
			t.Errorf("%s: %q %v", body, msg, err)
		}
		if b, _ := os.ReadFile(path); string(b) != body {
			t.Errorf("%s rewritten as %s", body, b)
		}
	}
	if msg, err := fixServerName(filepath.Join(dir, "missing.json")); msg != "" || err != nil {
		t.Errorf("missing file: %q %v", msg, err)
	}
}

func TestFatalExitIsNotRetried(t *testing.T) {
	d := fakeServer(t)
	d.AutoRestart = true
	if err := os.WriteFile(filepath.Join(d.Dir, "fail"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	in := New(d, slog.New(slog.NewTextHandler(io.Discard, nil)))
	crashes := make(chan Crash, 4)
	in.OnCrash = func(c Crash) { crashes <- c }
	if err := in.Start(); err != nil {
		t.Fatal(err)
	}
	waitState(t, in, Crashed)
	if v := in.View(); !strings.HasPrefix(v.LastError, "Config problem:") || v.ExitCode == nil || *v.ExitCode != 1 {
		t.Errorf("view %+v", v)
	}
	select {
	case c := <-crashes:
		if !c.Fatal || c.Code != 1 || c.Retry != 0 || !strings.HasPrefix(c.Reason, "Config problem:") {
			t.Errorf("crash %+v", c)
		}
	case <-time.After(5 * time.Second):
		t.Error("the crash was not reported")
	}
	time.Sleep(backoff[0] + 500*time.Millisecond) // past the first retry delay
	if in.State() != Crashed || in.View().PID != 0 {
		t.Error("a config error was retried")
	}
}

// A server that closes its console and runs on (2.0.6's headless Windows
// server) is ended, and not started again only to do the same.
func TestLostConsoleIsNotRetried(t *testing.T) {
	d := fakeServer(t)
	d.AutoRestart = true
	if err := os.WriteFile(filepath.Join(d.Dir, "mute"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	in := New(d, slog.New(slog.NewTextHandler(io.Discard, nil)))
	crashes := make(chan Crash, 4)
	in.OnCrash = func(c Crash) { crashes <- c }
	if err := in.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case c := <-crashes:
		if !c.Fatal || c.Retry != 0 || c.Reason != lostConsole {
			t.Errorf("crash %+v", c)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("a server with no console was left running")
	}
	if v := in.View(); in.State() != Crashed || v.PID != 0 || v.LastError != lostConsole {
		t.Errorf("state %s, view %+v", in.State(), v)
	}
}

// A server that dies on its own is reported as restarting; one stopped from
// the panel is not reported at all.
func TestCrashIsReported(t *testing.T) {
	d := fakeServer(t)
	d.AutoRestart = true
	in := New(d, slog.New(slog.NewTextHandler(io.Discard, nil)))
	crashes := make(chan Crash, 4)
	in.OnCrash = func(c Crash) { crashes <- c }
	if err := in.Start(); err != nil {
		t.Fatal(err)
	}
	waitState(t, in, Running)
	p, err := os.FindProcess(in.View().PID)
	if err != nil {
		t.Fatal(err)
	}
	p.Kill()
	select {
	case c := <-crashes:
		if c.Fatal || c.GaveUp || c.Retry != backoff[0] || c.Attempt != 1 || c.Of != len(backoff) {
			t.Errorf("crash %+v", c)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the crash was not reported")
	}
	waitState(t, in, Running) // restarted after the first delay
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := in.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case c := <-crashes:
		t.Errorf("a requested stop was reported: %+v", c)
	case <-time.After(200 * time.Millisecond):
	}
}
