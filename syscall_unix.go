//go:build linux || darwin

package xexec

import (
	"errors"
	"syscall"

	"golang.org/x/sys/unix"
)

// KillAll terminates the isolated process group for pid (managed processes have PGID=PID).
// It cleans up descendants in the group even after its leader exits; descendants that leave via setsid are excluded.
// If the group does not exist, it falls back to the individual process for callers without an isolated group.
// ESRCH is treated as idempotent success; other signal errors are returned.
// Values pid <= 1 are rejected before conversion or negation.
func KillAll(pid int32) error {
	if pid <= 1 {
		return &Error{Op: "kill all: invalid pid", PID: int64(pid)}
	}
	return killAll(int(pid), unix.Kill)
}

// killAll separates signal delivery from termination policy to make permission and other errors testable.
func killAll(pid int, kill func(int, syscall.Signal) error) error {
	groupErr := kill(-pid, unix.SIGKILL)
	if groupErr == nil {
		// The main process is in this group; do not signal its PID again, avoiding PID reuse after exit.
		return nil
	}
	var errs error
	if !errors.Is(groupErr, unix.ESRCH) {
		errs = &Error{Op: "kill process group", PID: int64(pid), Err: groupErr}
	}
	if err := kill(pid, unix.SIGKILL); err != nil && !errors.Is(err, unix.ESRCH) {
		errs = errors.Join(errs, &Error{Op: "kill process", PID: int64(pid), Err: err})
	}
	return errs
}
