//go:build windows

package xexec

import (
	"os"
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsNativeProcessHandleTerminationIsIdempotent(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestWindowsProcessTreeHelper$")
	cmd.Env = append(os.Environ(), windowsTreeHelperEnv+"=leaf")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	wait := windowsTestWaiter(t, cmd.Wait)
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = wait() })
	process, err := openWindowsTreeProcess(uint32(cmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	defer process.close()
	if process.created == 0 {
		t.Fatal("missing creation time")
	}
	if err := process.terminate(); err != nil {
		t.Fatal(err)
	}
	if err := wait(); err == nil {
		t.Fatal("terminated helper succeeded")
	}
	if err := process.terminate(); err != nil {
		t.Fatalf("already-exited process: %v", err)
	}
}

func TestWindowsFiletimeValue(t *testing.T) {
	if got := filetimeValue(windows.Filetime{HighDateTime: 1, LowDateTime: 2}); got != uint64(1)<<32|2 {
		t.Fatalf("filetime value = %d", got)
	}
}
