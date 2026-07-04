//go:build windows

package cli

import "syscall"

// detachSysProcAttr detaches the background update-check process. Windows
// has no setsid; CREATE_NEW_PROCESS_GROUP plus DETACHED_PROCESS (0x00000208)
// is the equivalent for a console-less fire-and-forget child.
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: 0x00000200 | 0x00000008}
}
