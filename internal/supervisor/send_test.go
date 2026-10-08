package supervisor

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	// The child of TestSendNeverBlocks: a server that never reads its console.
	if os.Getenv("SUPERVISOR_TEST_DEAF") == "1" {
		d, err := time.ParseDuration(os.Getenv("SUPERVISOR_TEST_SLEEP"))
		if err != nil {
			d = time.Minute
		}
		time.Sleep(d)
		os.Exit(0)
	}
	// The child of TestExitSeenThoughOutputIsHeld: a server that starts a
	// process holding its output, then exits.
	if os.Getenv("SUPERVISOR_TEST_FORK") == "1" {
		exe, _ := os.Executable()
		c := exec.Command(exe)
		c.Env = append(os.Environ(), "SUPERVISOR_TEST_DEAF=1", "SUPERVISOR_TEST_SLEEP=20s")
		c.Stdout, c.Stderr = os.Stdout, os.Stderr
		if err := c.Start(); err != nil {
			os.Exit(2)
		}
		os.Stdout.WriteString("forked\n")
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// The server's exit is seen even while something it started still holds its
// output: Done comes once the output has had a few seconds to drain.
func TestExitSeenThoughOutputIsHeld(t *testing.T) {
	t.Setenv("SUPERVISOR_TEST_FORK", "1")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	p, err := Start(Options{Exe: exe}, func(l string) { lines = append(lines, l) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Done():
	case <-time.After(drainTime + 10*time.Second):
		t.Fatal("the exit was never seen")
	}
	if p.ExitCode() != 0 || len(lines) == 0 || lines[0] != "forked" {
		t.Errorf("exit %d, lines %q", p.ExitCode(), lines)
	}
}

func TestCheckLine(t *testing.T) {
	for line, ok := range map[string]bool{
		"status":                    true,
		"say héllo 🛹 — ünïcode":     true,
		"":                          true,
		"say a\nquit":               false,
		"say a\rquit":               false,
		"say \x1a":                  false, // Ctrl-Z ends a Windows console's input
		"say \x00":                  false,
		"say \t":                    false,
		"say \x7f":                  false,
		"say \u0085":                false, // C1: next line
		"say \xff\xfe not utf-8":    false,
		"ban 76561198000000001 Joe": true,
	} {
		if err := CheckLine(line); (err == nil) != ok {
			t.Errorf("CheckLine(%q) = %v", line, err)
		}
	}
}

// A server that stops reading its console fills the pipe, and then the queue;
// Send says so instead of blocking whoever sent the line.
func TestSendNeverBlocks(t *testing.T) {
	t.Setenv("SUPERVISOR_TEST_DEAF", "1")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p, err := Start(Options{Exe: exe}, func(string) {}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Kill()
	line := strings.Repeat("x", 64<<10)
	sent := make(chan error, 1)
	go func() {
		var err error
		for i := 0; i < 1000 && err == nil; i++ {
			err = p.Send(line)
		}
		sent <- err
	}()
	select {
	case err := <-sent:
		if err == nil {
			t.Fatal("64 MB queued for a server that reads none of it")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Send blocked")
	}
}
