//go:build linux || freebsd

package xexec

import "syscall"

// sysProcAttr builds process attributes for platforms supporting Pdeathsig (Linux, FreeBSD).
//
// In addition to Setpgid (a new process group for Kill(-pid) cleanup), it sets
// Pdeathsig=SIGKILL so the kernel sends SIGKILL to the child when its parent thread dies.
// This covers cases cmd.Cancel cannot handle: a Go crash, kill -9, or abrupt panic exit,
// where defer cancel() cannot run and the child might otherwise survive as an orphan.
//
// Scope and limitations:
//   - Pdeathsig does not determine exit-code reliability; the wrapper records script-level codes
//     in a file (see execSysScript). Pdeathsig is for cleanup, not exit-code collection.
//   - Pdeathsig signals a single process: only Go's direct child (the sh wrapper).
//     It cannot reach grandchildren forked by the wrapper or target (such as cmd &),
//     which may survive a parent crash. Whole-tree cleanup requires Kill(-pgid), but
//     Pdeathsig cannot target a process group; use deployment-level supervision for that guarantee.
//     Normal cancellation/timeouts use cmd.Cancel's Kill(-pid) and do not have this limitation.
//
// Implementation note: Pdeathsig tracks the OS thread that created the child, not the entire Go
// process. This library does not use runtime.LockOSThread: holding an OS thread for the full
// script lifetime is expensive for long-running scripts. In practice, the Go runtime rarely retires
// created threads, so premature child termination while the Go process is alive is unlikely.
// processSysProcAttr preserves SIGTERM-on-parent-thread-exit behavior for long-running commands.
func processSysProcAttr() *syscall.SysProcAttr {
	attr := sysProcAttr()
	attr.Pdeathsig = syscall.SIGTERM
	return attr
}

func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Setpgid:   true,
		Pdeathsig: syscall.SIGKILL,
	}
}
