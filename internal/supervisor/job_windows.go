//go:build windows

package supervisor

import (
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// One job for the manager: when the manager exits (or crashes) Windows closes
// the handle and kills every server in it, so no orphan keeps the ports.
var (
	jobOnce sync.Once
	job     windows.Handle
	jobErr  error
)

func managerJob() (windows.Handle, error) {
	jobOnce.Do(func() {
		job, jobErr = windows.CreateJobObject(nil, nil)
		if jobErr != nil {
			return
		}
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
		info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
		_, jobErr = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	})
	return job, jobErr
}

func configure(cmd *exec.Cmd) {
	// No console of its own, and its own process group so a Ctrl+C in the
	// manager's console is not delivered to the server too: the manager stops
	// servers itself with "quit".
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP,
		HideWindow:    true,
	}
}

func attach(cmd *exec.Cmd) error {
	j, err := managerJob()
	if err != nil {
		return err
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return windows.AssignProcessToJobObject(j, h)
}

// kill ends the server. What it started stays in the manager's job, which
// ends them when the manager exits.
func kill(cmd *exec.Cmd) { _ = cmd.Process.Kill() }
