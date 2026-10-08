//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// relaunch starts the new manager in its own session, on the same terminal.
func relaunch(exe string, args []string) error {
	cmd := exec.Command(exe, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// stderrIsJournal reports whether stderr is the journal stream systemd set
// JOURNAL_STREAM to, as device:inode.
func stderrIsJournal() bool {
	want := os.Getenv("JOURNAL_STREAM")
	var st syscall.Stat_t
	if want == "" || syscall.Fstat(int(os.Stderr.Fd()), &st) != nil {
		return false
	}
	return want == fmt.Sprintf("%d:%d", st.Dev, st.Ino)
}
