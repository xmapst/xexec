//go:build linux

// Package xexec Linux orphan reaping supports applications running as container PID 1.
// An application running as PID 1 adopts orphan processes and must reap them
// with wait syscalls to prevent zombie process table entries.
// The reaper scans on SIGCHLD and performs nonblocking waits on unowned zombie PIDs.
package xexec

import (
	"golang.org/x/sys/unix"
)

// enableChildSubReaper marks the current process as a child subreaper.
// Orphans are normally adopted by init (PID 1). PR_SET_CHILD_SUBREAPER instead
// allows this process to adopt orphaned descendants and collect their statuses.
// This also supports internal reaper tests running outside a PID namespace.
// Automatic production startup remains restricted to PID 1.
func enableChildSubReaper() error {
	return unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0)
}
