//go:build linux

package xexec

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
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
				_ = unix.Kill(-cmd.Process.Pid, unix.SIGKILL)
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
