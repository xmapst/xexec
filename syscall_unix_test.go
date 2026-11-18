//go:build linux || darwin

package xexec

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

func TestKillAllInvalidPID(t *testing.T) {
	for _, pid := range []int32{-1 << 31, -2, -1, 0, 1} {
		if err := KillAll(pid); err == nil {
			t.Errorf("accepted invalid pid %d", pid)
		}
	}
}

func TestKillAllSignalErrors(t *testing.T) {
	for _, tc := range []struct {
		name      string
		groupErr  error
		pidErr    error
		wantErr   error
		wantCalls int
	}{
		{"group-success", nil, unix.EPERM, nil, 1},
		{"both-missing", unix.ESRCH, unix.ESRCH, nil, 2},
		{"ungrouped-process", unix.ESRCH, nil, nil, 2},
		{"group-denied", unix.EPERM, nil, unix.EPERM, 2},
		{"process-denied", unix.ESRCH, unix.EPERM, unix.EPERM, 2},
		{"both-denied", unix.EPERM, unix.EACCES, unix.EACCES, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			err := killAll(123, func(pid int, sig unix.Signal) error {
				calls++
				wantPID := -123
				if calls == 2 {
					wantPID = 123
				}
				if pid != wantPID || sig != unix.SIGKILL {
					t.Fatalf("unexpected signal: pid=%d sig=%v", pid, sig)
				}
				if pid < 0 {
					return tc.groupErr
				}
				return tc.pidErr
			})
			if !errors.Is(err, tc.wantErr) || calls != tc.wantCalls {
				t.Fatalf("err=%v calls=%d; want err=%v calls=%d", err, calls, tc.wantErr, tc.wantCalls)
			}
			if errors.Is(tc.groupErr, unix.EPERM) && !errors.Is(err, unix.EPERM) {
				t.Fatalf("lost group error: %v", err)
			}
		})
	}
}
