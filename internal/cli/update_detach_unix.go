//go:build !windows

package cli

import "syscall"

// detachSysProcAttr fully detaches the background update-check process from
// the controlling terminal so it survives the parent exiting.
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
