package supervisor

import (
	"os/exec"
	"syscall"
)

func configure(cmd *exec.Cmd) {
	// Own process group, so a Ctrl+C in the manager's terminal does not reach
	// the server; Pdeathsig kills it if the manager dies.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
}

func attach(*exec.Cmd) error { return nil }

// kill ends the server and anything it started: it leads its own process
// group, which takes all of them at once.
func kill(cmd *exec.Cmd) {
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		_ = cmd.Process.Kill()
	}
}
