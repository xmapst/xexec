//go:build linux

package xexec

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestIndependentProcessGroupCancellation(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd, err := ExecContext(ctx, "/bin/sh", WithExecArgs("-c", "sleep 30 & echo $! > \"$CHILD_PID_FILE\"; wait"), WithExecEnv("CHILD_PID_FILE="+os.Getenv("TMPDIR")+"/child"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = cmd.Wait() }()
	pgid, err := unix.Getpgid(cmd.Process.Pid)
	if err != nil || pgid != cmd.Process.Pid || pgid == unix.Getpgrp() {
		t.Fatalf("invalid group: %d %v", pgid, err)
	}
	var child int
	deadline := time.After(5 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for child == 0 {
		data, err := os.ReadFile(os.Getenv("TMPDIR") + "/child")
		if err == nil {
			child, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		}
		if child > 0 {
			break
		}
		select {
		case <-ticker.C:
		case <-deadline:
			t.Fatal("child not ready")
		}
	}
	cancel()
	// The descendant may briefly be a zombie, but must not remain alive.
	for {
		data, err := os.ReadFile("/proc/" + strconv.Itoa(child) + "/stat")
		if os.IsNotExist(err) {
			break
		}
		end := strings.LastIndexByte(string(data), ')')
		if end >= 0 && strings.HasPrefix(string(data)[end+1:], " Z ") {
			break
		}
		select {
		case <-ticker.C:
		case <-deadline:
			t.Fatal("descendant survived cancellation")
		}
	}
}

func TestExecWaitBoundsInheritedOutput(t *testing.T) {
	for _, deadline := range []time.Duration{0, 100 * time.Millisecond} {
		t.Run(deadline.String(), func(t *testing.T) {
			ctx := context.Background()
			if deadline > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, deadline)
				defer cancel()
			}
			pidFile := filepath.Join(t.TempDir(), "child")
			start := time.Now()
			cmd, err := ExecContext(ctx, "/bin/sh",
				WithExecArgs("-c", `sleep 10 & echo $! > "$CHILD_PID_FILE"`),
				WithExecEnv("CHILD_PID_FILE="+pidFile))
			if err != nil {
				t.Fatal(err)
			}
			defer KillAll(int32(cmd.Process.Pid))
			defer os.Remove(cmd.output.Name())
			err = cmd.Wait()
			if elapsed := time.Since(start); elapsed > 2*time.Second {
				t.Fatalf("Wait took %s", elapsed)
			}
			if !errors.Is(err, exec.ErrWaitDelay) {
				t.Fatalf("expected bounded drain error, got %v", err)
			}
			if deadline > 0 && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("missing context error: %v", err)
			}
			data, err := os.ReadFile(pidFile)
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil || pid <= 1 {
				t.Fatalf("invalid descendant PID %q", data)
			}
			until := time.Now().Add(time.Second)
			for {
				stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
				if os.IsNotExist(err) {
					break
				}
				end := strings.LastIndexByte(string(stat), ')')
				if end >= 0 && strings.HasPrefix(string(stat)[end+1:], " Z ") {
					break
				}
				if time.Now().After(until) {
					t.Fatal("descendant survived bounded output drain")
				}
				<-time.After(10 * time.Millisecond)
			}
		})
	}
}
