//go:build linux

package xexec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Run this test binary directly as the container entrypoint, without docker --init
// or an intermediate shell. Normal host runs skip this integration suite.
func TestDockerPID1(t *testing.T) {
	if os.Getenv("XEXEC_DOCKER_PID1") != "1" {
		t.Skip("run test/test-docker-pid1.sh to test the library as Docker PID 1")
	}
	if os.Getpid() != 1 {
		t.Fatalf("integration test must itself be PID 1, got PID %d", os.Getpid())
	}
	t.Run("lazy-start-and-concurrent-exit-status", func(t *testing.T) {
		// Do not call defaultReaper.start: exercise the PID 1 lazy startup path.
		var wg sync.WaitGroup
		for i := range 40 {
			wg.Go(func() {
				code := i%19 + 1
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				cmd, err := ExecContext(ctx, "/bin/sh", WithExecArgs("-c", fmt.Sprintf("exit %d", code)))
				if err != nil {
					t.Error(err)
					return
				}
				path := cmd.output.Name()
				defer os.Remove(path)
				err = cmd.Wait()
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) || cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != code {
					t.Errorf("lost managed exit status %d: %v", code, err)
				}
			})
		}
		wg.Wait()
		defaultReaper.mu.Lock()
		count := len(defaultReaper.managed)
		defaultReaper.mu.Unlock()
		if count != 0 {
			t.Fatalf("wait ownership leaked: %d entries", count)
		}
	})
	t.Run("adopt-and-reap-orphan", func(t *testing.T) {
		pid := spawnDockerTestOrphan(t, "0.1")
		waitDockerPIDGone(t, pid)
	})
	t.Run("live-orphan-is-not-killed", func(t *testing.T) {
		pid := spawnDockerTestOrphan(t, "10")
		defer func() {
			_ = unix.Kill(pid, unix.SIGKILL)
			waitDockerPIDGone(t, pid)
		}()
		// Force multiple real scans while the adopted child remains alive.
		for range 3 {
			if err := defaultReaper.scanChildren(); err != nil {
				t.Fatal(err)
			}
		}
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			t.Fatalf("live orphan disappeared: %v", err)
		}
		end := strings.LastIndexByte(string(data), ')')
		fields := strings.Fields(string(data)[end+1:])
		if end < 0 || len(fields) < 2 || fields[0] == "Z" || fields[1] != "1" {
			t.Fatalf("expected live orphan adopted by PID 1: %q", data)
		}
	})
	t.Run("opaque-wait-preserves-status", func(t *testing.T) {
		release := defaultReaper.beginOpaqueWait()
		defer release()
		cmd := exec.Command("/bin/sh", "-c", "exit 29")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		defer cmd.Wait()
		waitForTestZombie(t, defaultReaper, cmd.Process.Pid)
		if err := defaultReaper.scanChildren(); err != nil {
			t.Fatal(err)
		}
		err := cmd.Wait()
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 29 {
			t.Fatalf("opaque status lost: %v", err)
		}
		release()
		release()
	})
	t.Run("script-exit-code-and-timeout", func(t *testing.T) {
		code, _, err := ExecScriptContent(context.Background(), "sh", "#!/bin/sh\nexit 17\n", WithScriptLogger(&captureTestLogger{}))
		var exitErr *exec.ExitError
		if code != 17 || !errors.As(err, &exitErr) || exitErr.ExitCode() != 17 {
			t.Fatalf("PID 1 script status: code=%d err=%v", code, err)
		}
		pidFile := filepath.Join(t.TempDir(), "child")
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		code, _, err = ExecScriptContent(ctx, "sh", "#!/bin/sh\nsleep 10 & echo $! > \"$CHILD_PID_FILE\"; wait\n", WithScriptEnv("CHILD_PID_FILE="+pidFile), WithScriptLogger(&captureTestLogger{}))
		if code != 137 || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("PID 1 cancellation: code=%d err=%v", code, err)
		}
		data, err := os.ReadFile(pidFile)
		if err != nil {
			t.Fatal(err)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil || pid <= 1 {
			t.Fatalf("invalid descendant PID %q", data)
		}
		waitDockerPIDGone(t, pid)
	})
}

func spawnDockerTestOrphan(t *testing.T, duration string) int {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "sleep "+duration+" >/dev/null 2>&1 & echo $!")
	var output strings.Builder
	cmd.Stdout = &output
	release, err := defaultReaper.startManaged(cmd)
	if err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	release()
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(output.String()))
	if err != nil || pid <= 1 {
		t.Fatalf("invalid orphan PID %q", output.String())
	}
	return pid
}

func waitDockerPIDGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); os.IsNotExist(err) {
			return
		}
		<-time.After(10 * time.Millisecond)
	}
	t.Fatalf("PID %d was not fully reaped (zombies must disappear too)", pid)
}
