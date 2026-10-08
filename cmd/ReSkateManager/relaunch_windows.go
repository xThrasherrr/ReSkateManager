//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// relaunch starts the new manager in its own console window.
func relaunch(exe string, args []string) error {
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000010 | syscall.CREATE_NEW_PROCESS_GROUP} // CREATE_NEW_CONSOLE
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// stderrIsJournal: Windows has no systemd.
func stderrIsJournal() bool { return false }
