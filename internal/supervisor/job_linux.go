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
