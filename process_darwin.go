//go:build darwin

package xexec

import (
	"golang.org/x/sys/unix"
)

// sysProcAttr builds process attributes on macOS without Pdeathsig.
//
// macOS has no Pdeathsig field in unix.SysProcAttr, so the kernel cannot
// terminate the child on parent death this way. Setpgid makes the child a new group leader;
// Kill(-pid, SIGKILL) on cancellation terminates the group. If Go crashes or is killed,
// children may survive as orphans, matching this library's earlier behavior on all platforms.
func processSysProcAttr() *unix.SysProcAttr { return sysProcAttr() }

func sysProcAttr() *unix.SysProcAttr {
	return &unix.SysProcAttr{
		Setpgid: true,
	}
}
