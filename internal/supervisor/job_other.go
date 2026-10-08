//go:build !windows && !linux

package supervisor

import (
	"os/exec"
	"syscall"
)

func configure(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

func attach(*exec.Cmd) error { return nil }

// kill ends the server and anything it started: it leads its own process
// group, which takes all of them at once.
func kill(cmd *exec.Cmd) {
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		_ = cmd.Process.Kill()
	}
}
