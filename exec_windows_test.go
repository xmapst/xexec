//go:build windows

package xexec

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const windowsTreeHelperEnv = "XEXEC_WINDOWS_TREE_HELPER"

// The helper uses only this test executable; no ping, PowerShell, or network is needed.
func TestWindowsProcessTreeHelper(t *testing.T) {
	switch os.Getenv(windowsTreeHelperEnv) {
	case "leaf":
		for {
			time.Sleep(time.Hour)
		}
	case "root":
		child := exec.Command(os.Args[0], "-test.run=^TestWindowsProcessTreeHelper$")
		child.Env = append(os.Environ(), windowsTreeHelperEnv+"=leaf")
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		data, _ := json.Marshal([]int{os.Getpid(), child.Process.Pid})
		if err := os.WriteFile(os.Getenv("XEXEC_WINDOWS_TREE_READY"), data, 0600); err != nil {
			_ = child.Process.Kill()
			_ = child.Wait()
			os.Exit(3)
		}
		_ = child.Wait()
		os.Exit(0)
	}
}

// Keep handles, not only PIDs: assertions and last-resort cleanup cannot target
// an unrelated process if Windows reuses a PID after the child exits.
func windowsTestProcessHandle(t *testing.T, pid int) windows.Handle {
	t.Helper()
	h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		t.Fatalf("open owned process %d: %v", pid, err)
	}
	t.Cleanup(func() {
		state, _ := windows.WaitForSingleObject(h, 0)
		if state == uint32(windows.WAIT_TIMEOUT) {
			_ = windows.TerminateProcess(h, 1)
			_, _ = windows.WaitForSingleObject(h, 5000)
		}
		_ = windows.CloseHandle(h)
	})
	return h
}

func windowsTestAwaitTree(t *testing.T, path string) []windows.Handle {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		var pids []int
		if err == nil && json.Unmarshal(data, &pids) == nil && len(pids) == 2 {
			var handles []windows.Handle
			for _, pid := range pids {
				if pid <= 0 {
					t.Fatalf("invalid helper PID: %d", pid)
				}
				handles = append(handles, windowsTestProcessHandle(t, pid))
			}
			return handles
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("process tree did not become ready")
	return nil
}

func windowsTestAssertTreeStopped(t *testing.T, handles []windows.Handle) {
	t.Helper()
	for _, h := range handles {
		state, err := windows.WaitForSingleObject(h, 5000)
		if err != nil || state != windows.WAIT_OBJECT_0 {
			t.Errorf("owned process still running: wait state=%d err=%v", state, err)
		}
	}
}

func windowsTestWaiter(t *testing.T, wait func() error) func() error {
	var once sync.Once
	done := make(chan struct{})
	var result error
	return func() error {
		once.Do(func() {
			go func() {
				result = wait()
				close(done)
			}()
		})
		select {
		case <-done:
			return result
		case <-time.After(10 * time.Second):
			t.Fatal("Wait did not finish after process-tree termination")
			return nil
		}
	}
}

func TestWindowsKillAllTerminatesOwnedTree(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready.json")
	cmd := exec.Command(os.Args[0], "-test.run=^TestWindowsProcessTreeHelper$")
	cmd.Env = append(os.Environ(), windowsTreeHelperEnv+"=root", "XEXEC_WINDOWS_TREE_READY="+ready)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	root := windowsTestProcessHandle(t, cmd.Process.Pid)
	wait := windowsTestWaiter(t, cmd.Wait)
	t.Cleanup(func() {
		if state, _ := windows.WaitForSingleObject(root, 0); state == uint32(windows.WAIT_TIMEOUT) {
			_ = KillAll(int32(cmd.Process.Pid))
		}
		_ = cmd.Process.Kill()
		_ = wait()
	})
	handles := windowsTestAwaitTree(t, ready)
	// Native cleanup must work without an external TASKKILL executable in PATH.
	t.Setenv("PATH", t.TempDir())
	if err := KillAll(int32(cmd.Process.Pid)); err != nil {
		t.Fatalf("KillAll: %v", err)
	}
	if err := wait(); err == nil {
		t.Fatal("force-killed root unexpectedly succeeded")
	}
	windowsTestAssertTreeStopped(t, handles)
}

func TestWindowsExecCancellationTerminatesOwnedTree(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready.json")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd, err := ExecContext(ctx, os.Args[0], WithExecArgs("-test.run=^TestWindowsProcessTreeHelper$"),
		WithExecEnv(windowsTreeHelperEnv+"=root", "XEXEC_WINDOWS_TREE_READY="+ready))
	if err != nil {
		t.Fatal(err)
	}
	logPath := cmd.output.Name()
	root := windowsTestProcessHandle(t, cmd.Process.Pid)
	wait := windowsTestWaiter(t, cmd.Wait)
	t.Cleanup(func() {
		if state, _ := windows.WaitForSingleObject(root, 0); state == uint32(windows.WAIT_TIMEOUT) {
			_ = KillAll(int32(cmd.Process.Pid))
		}
		cancel()
		_ = cmd.Process.Kill()
		_ = wait()
		_ = os.Remove(logPath)
	})
	handles := windowsTestAwaitTree(t, ready)
	cancel()
	if err := wait(); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
	windowsTestAssertTreeStopped(t, handles)
}

func TestWindowsScriptCancellationTerminatesOwnedTree(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready.json")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	type result struct {
		code int
		err  error
	}
	done := make(chan result, 1)
	go func() {
		code, _, err := ExecScriptContent(ctx, "bat", "@echo off\r\n\"%XEXEC_TREE_EXECUTABLE%\" -test.run=^TestWindowsProcessTreeHelper$\r\n",
			WithScriptEnv(windowsTreeHelperEnv+"=root", "XEXEC_WINDOWS_TREE_READY="+ready, "XEXEC_TREE_EXECUTABLE="+os.Args[0]),
			WithScriptLogger(&captureTestLogger{}))
		done <- result{code, err}
	}()
	handles := windowsTestAwaitTree(t, ready)
	cancel()
	select {
	case got := <-done:
		if got.code != 137 || !errors.Is(got.err, context.Canceled) {
			t.Fatalf("script cancellation: code=%d err=%v", got.code, got.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("script cancellation did not finish")
	}
	windowsTestAssertTreeStopped(t, handles)
}

func TestWindowsScriptCancelFallsBackWhenTreeCleanupFails(t *testing.T) {
	// Use a leaf only: this tests the documented root-only fallback, not tree cleanup.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWindowsProcessTreeHelper$")
	cmd.Env = append(os.Environ(), windowsTreeHelperEnv+"=leaf")
	s := &script{cmd: cmd, exitCodePath: filepath.Join(t.TempDir(), "exit-code"), logger: &captureTestLogger{}}
	sentinel := errors.New("injected native tree cleanup failure")
	s.beforeExecWithKillAll(func(pid int32) error {
		if cmd.Process == nil || pid != int32(cmd.Process.Pid) {
			t.Errorf("cleanup called with unexpected PID %d", pid)
		}
		return sentinel
	})
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	h := windowsTestProcessHandle(t, cmd.Process.Pid)
	wait := windowsTestWaiter(t, cmd.Wait)
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = wait() })
	if err := cmd.Cancel(); !errors.Is(err, sentinel) {
		t.Fatalf("tree cleanup failure was discarded: %v", err)
	}
	if err := wait(); err == nil {
		t.Fatal("fallback-killed process unexpectedly succeeded")
	}
	windowsTestAssertTreeStopped(t, []windows.Handle{h})
	data, err := os.ReadFile(s.exitCodePath)
	if err != nil || string(data) != "137" {
		t.Fatalf("fallback exit-code file = %q, err=%v", data, err)
	}
}
