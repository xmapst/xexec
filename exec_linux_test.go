//go:build linux

package xexec

import (
	"context"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
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
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil || pgid != cmd.Process.Pid || pgid == syscall.Getpgrp() {
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
