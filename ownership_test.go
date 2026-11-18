package xexec

import (
	"os"
	"os/exec"
	"testing"
)

func TestReleaseDoesNotDeleteReusedPID(t *testing.T) {
	const helperEnv = "XEXEC_OWNERSHIP_HELPER"
	if os.Getenv(helperEnv) == "1" {
		os.Exit(0)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestReleaseDoesNotDeleteReusedPID$")
	cmd.Env = append(os.Environ(), helperEnv+"=1")
	release, err := defaultReaper.startManaged(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	replacement := exec.Command(os.Args[0])
	// Simulate a new owner registering the reused PID before the old release runs.
	defaultReaper.mu.Lock()
	defaultReaper.managed[cmd.Process.Pid] = replacement
	defaultReaper.mu.Unlock()
	release()
	defaultReaper.mu.Lock()
	got := defaultReaper.managed[cmd.Process.Pid]
	delete(defaultReaper.managed, cmd.Process.Pid)
	defaultReaper.mu.Unlock()
	if got != replacement {
		t.Fatal("old release erased new PID owner")
	}
}
