//go:build linux

package xexec

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestReviewKillAllDescendant(t *testing.T) {
	for _, leaderExited := range []bool{false, true} {
		name := "running-leader"
		if leaderExited {
			name = "exited-leader"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("TMPDIR", t.TempDir())
			file := os.Getenv("TMPDIR") + "/child"
			script := "sleep 30 & echo $! > \"$CHILD_PID_FILE\"; wait"
			if leaderExited {
				script = "sleep 30 >/dev/null 2>&1 & echo $! > \"$CHILD_PID_FILE\""
			}
			cmd, err := ExecContext(context.Background(), "/bin/sh", WithExecArgs("-c", script), WithExecEnv("CHILD_PID_FILE="+file))
			if err != nil {
				t.Fatal(err)
			}
			waited := false
			defer func() {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				if !waited {
					_ = cmd.Wait()
				}
			}()
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			deadline := time.After(3 * time.Second)
			var child int
			for child == 0 {
				data, _ := os.ReadFile(file)
				child, _ = strconv.Atoi(strings.TrimSpace(string(data)))
				if child > 0 {
					break
				}
				select {
				case <-ticker.C:
				case <-deadline:
					t.Fatal("child not ready")
				}
			}
			if leaderExited {
				err := cmd.Wait()
				waited = true
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := KillAll(int32(cmd.Process.Pid)); err != nil {
				t.Fatal(err)
			}
			for {
				data, err := os.ReadFile("/proc/" + strconv.Itoa(child) + "/stat")
				if os.IsNotExist(err) {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				end := strings.LastIndexByte(string(data), ')')
				if end < 0 {
					t.Fatalf("invalid stat: %q", data)
				}
				if strings.HasPrefix(string(data)[end+1:], " Z ") {
					break
				}
				select {
				case <-ticker.C:
				case <-deadline:
					t.Fatalf("KillAll returned success but descendant %d remains alive: %s", child, data)
				}
			}
			if !waited {
				_ = cmd.Wait()
				waited = true
			}
			if err := KillAll(int32(cmd.Process.Pid)); err != nil {
				t.Fatalf("repeated kill: %v", err)
			}
		})
	}
}

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
		{"group-success", nil, syscall.EPERM, nil, 1},
		{"both-missing", syscall.ESRCH, syscall.ESRCH, nil, 2},
		{"ungrouped-process", syscall.ESRCH, nil, nil, 2},
		{"group-denied", syscall.EPERM, nil, syscall.EPERM, 2},
		{"process-denied", syscall.ESRCH, syscall.EPERM, syscall.EPERM, 2},
		{"both-denied", syscall.EPERM, syscall.EACCES, syscall.EACCES, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			err := killAll(123, func(pid int, sig syscall.Signal) error {
				calls++
				wantPID := -123
				if calls == 2 {
					wantPID = 123
				}
				if pid != wantPID || sig != syscall.SIGKILL {
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
			if tc.groupErr == syscall.EPERM && !errors.Is(err, syscall.EPERM) {
				t.Fatalf("lost group error: %v", err)
			}
		})
	}
}
