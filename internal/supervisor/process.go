// Package supervisor runs one ReSkateServer process with piped stdin/stdout.
package supervisor

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"
)

// Options are how to start a server.
type Options struct {
	Exe  string   // full path to ReSkateServer(.exe)
	Dir  string   // working directory (the server resolves everything from its exe folder anyway)
	Args []string // extra arguments; --no-update is always added
}

// Process is a running server. Lines are delivered to the onLine callback
// from a single goroutine, in order. onIdle runs when stdout has been quiet
// for IdleGap after a line, so a caller can complete multi-line entries.
type Process struct {
	cmd  *exec.Cmd
	in   chan string // console lines, written to stdin in order by one goroutine
	done chan struct{}
	pid  int
}

// inQueue is how many console lines may wait for the server to read them.
// The manager sends one command at a time, so a full queue means the server
// stopped reading its console.
const inQueue = 16

// IdleGap is how long output must pause after a line for onIdle to be called.
const IdleGap = 40 * time.Millisecond

// drainTime is how long output may go on arriving once the server exited.
const drainTime = 5 * time.Second

// Start runs the server, calling onLine with each line it prints, in order,
// and onIdle when its output has paused for IdleGap.
func Start(o Options, onLine func(string), onIdle func()) (*Process, error) {
	args := append([]string{"--no-update"}, o.Args...)
	cmd := exec.Command(o.Exe, args...)
	cmd.Dir = o.Dir
	configure(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	// A pipe of its own rather than StdoutPipe, so the exit is seen when it
	// happens: a child the server started could hold StdoutPipe's open after
	// the server is gone, and Wait (and so Done) would never come.
	stdout, w, err := os.Pipe()
	if err != nil {
		stdin.Close()
		return nil, err
	}
	cmd.Stdout = w
	cmd.Stderr = w // the server writes nothing to stderr, but keep it if it ever does
	err = cmd.Start()
	w.Close() // the server has its own copy now
	if err != nil {
		stdout.Close()
		return nil, fmt.Errorf("start %s: %w", o.Exe, err)
	}
	if err := attach(cmd); err != nil {
		kill(cmd)
		_ = cmd.Wait()
		stdout.Close()
		return nil, fmt.Errorf("attach job: %w", err)
	}
	p := &Process{cmd: cmd, in: make(chan string, inQueue), done: make(chan struct{}), pid: cmd.Process.Pid}
	// The only writer to stdin. A write can block for as long as the server
	// doesn't read, and must not block whoever sent the line meanwhile; it
	// ends once the process exits, when Wait closes the pipe under it.
	go func() {
		for {
			select {
			case line := <-p.in:
				if _, err := io.WriteString(stdin, line+"\n"); err != nil {
					return
				}
			case <-p.done:
				return
			}
		}
	}()

	lines := make(chan string, 256)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		defer close(lines)
		readLines(stdout, lines)
	}()
	waited := make(chan struct{})
	go func() {
		_ = cmd.Wait() // ExitCode reads what it found
		close(waited)
		// What the server wrote before it exited is still on its way. Past a
		// few seconds, whatever holds the pipe open isn't the server.
		select {
		case <-readDone:
		case <-time.After(drainTime):
		}
		stdout.Close()
	}()
	go func() {
		idle := time.NewTimer(time.Hour)
		idle.Stop()
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					idle.Stop()
					if onIdle != nil {
						onIdle()
					}
					<-waited
					close(p.done)
					return
				}
				onLine(l)
				idle.Reset(IdleGap)
			case <-idle.C:
				if onIdle != nil {
					onIdle()
				}
			}
		}
	}()
	return p, nil
}

// MaxLine is the longest line kept. A longer one is cut there and the rest of
// it skipped, so one runaway line can neither stop the reader nor use
// unbounded memory.
const MaxLine = 1 << 20

// cutMark ends a line that was cut at MaxLine.
const cutMark = " … (cut: the line was over 1 MiB)"

// readLines sends each line read from r to out, without its line ending, until
// r ends. bufio.Scanner would give up for good at a line over its limit; then
// nothing drains the pipe, and the server blocks on its next write.
func readLines(r io.Reader, out chan<- string) {
	br := bufio.NewReaderSize(r, 64<<10)
	var line []byte
	cut := false
	for {
		chunk, err := br.ReadSlice('\n')
		end := err == nil // chunk holds the rest of the line, newline included
		if end {
			chunk = chunk[:len(chunk)-1]
		}
		if room := MaxLine - len(line); room >= len(chunk) {
			line = append(line, chunk...)
		} else {
			line, cut = append(line, chunk[:room]...), true
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if end || len(line) > 0 {
			s := strings.TrimSuffix(string(line), "\r")
			if cut {
				s += cutMark
			}
			out <- s
		}
		line, cut = line[:0], false
		if !end {
			return // the pipe closed, or broke
		}
	}
}

// PID is the process's ID.
func (p *Process) PID() int { return p.pid }

// Usage is what the process has used so far.
type Usage struct {
	CPU time.Duration // user + kernel time since it started
	Mem uint64        // resident memory (working set on Windows), bytes
}

// Usage reads the process's CPU time and memory from the OS.
func (p *Process) Usage() (Usage, error) { return usage(p.pid) }

// UsageOf reads any process's CPU time and memory, the manager's own included.
func UsageOf(pid int) (Usage, error) { return usage(pid) }

// Done is closed once the process has exited and all its output was delivered.
func (p *Process) Done() <-chan struct{} { return p.done }

// ExitCode is valid after Done; -1 when the process was killed or never ran.
func (p *Process) ExitCode() int {
	if p.cmd.ProcessState == nil {
		return -1
	}
	return p.cmd.ProcessState.ExitCode()
}

var errNotRunning = errors.New("server is not running")

// Send queues one console line for the server. It refuses a line holding a
// control character: a newline would start a second command, and on Windows
// a Ctrl-Z would end the server's console input for good.
func (p *Process) Send(line string) error {
	if err := CheckLine(line); err != nil {
		return err
	}
	select {
	case <-p.done:
		return errNotRunning
	default:
	}
	select {
	case p.in <- line:
		return nil
	case <-p.done:
		return errNotRunning
	default:
		return errors.New("the server isn't reading its console")
	}
}

// CheckLine reports why line can't be a console line: one holding a control
// character (C0, DEL or C1) or bytes that aren't UTF-8, which the server
// refuses in chat as well.
func CheckLine(line string) error {
	if !utf8.ValidString(line) {
		return errors.New("a command must be UTF-8 text")
	}
	for _, r := range line {
		switch {
		case r == '\n' || r == '\r':
			return errors.New("a command must be one line")
		case r < 0x20 || r == 0x7f || r >= 0x80 && r <= 0x9f:
			return fmt.Errorf("a command can't hold control characters (%U)", r)
		}
	}
	return nil
}

// Stop asks the server to quit (it logs off Steam and tells players), then kills it after timeout.
func (p *Process) Stop(timeout time.Duration) {
	_ = p.Send("quit")
	select {
	case <-p.done:
		return
	case <-time.After(timeout):
	}
	kill(p.cmd)
	<-p.done
}

// Kill ends the process at once.
func (p *Process) Kill() {
	kill(p.cmd)
	<-p.done
}
