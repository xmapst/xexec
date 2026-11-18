//go:build !windows && !linux && !freebsd

package xexec

import "syscall"

// sysProcAttr builds process attributes on Unix platforms without Pdeathsig (macOS, OpenBSD, NetBSD,
// DragonFly, Solaris, etc.).
//
// These platforms have no Pdeathsig field in syscall.SysProcAttr, so the kernel cannot
// terminate the child on parent death this way. Setpgid makes the child a new group leader;
// Kill(-pid, SIGKILL) on cancellation terminates the group. If Go crashes or is killed,
// children may survive as orphans, matching this library's earlier behavior on all platforms.
func processSysProcAttr() *syscall.SysProcAttr { return sysProcAttr() }

func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Setpgid: true,
	}
}
