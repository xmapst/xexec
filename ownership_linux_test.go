//go:build linux

package xexec

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPID1Ownership(t *testing.T) {
	if os.Getenv("REAPER_OWNERSHIP_HELPER") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPID1Ownership$")
		cmd.Env = append(os.Environ(), "REAPER_OWNERSHIP_HELPER=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("helper: %v\n%s", err, out)
		}
		return
	}
	if os.Getpid() != 1 {
		defaultReaper.start()
	} // PID 1 relies on lazy startup rather than an explicit reaper start.
	var wg sync.WaitGroup
	for i := range 100 {
		wg.Add(1)
		go func(code int) {
			defer wg.Done()
			cmd := exec.Command("/bin/sh", "-c", fmt.Sprintf("exit %d", code))
			release, err := defaultReaper.startManaged(cmd)
			if err != nil {
				t.Error(err)
				return
			}
			defer release()
			err = cmd.Wait()
			if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != code {
				t.Errorf("lost status %d: %v", code, err)
			}
		}(i % 20)
	}
	wg.Wait()
	// Keep an opaque child status intact across a periodic scan.
	release := defaultReaper.beginOpaqueWait()
	cmd := exec.Command("/bin/sh", "-c", "exit 19")
	if err := cmd.Start(); err != nil {
		release()
		t.Fatal(err)
	}
	<-time.After(1200 * time.Millisecond)
	err := cmd.Wait()
	release()
	release() // Release is idempotent.
	if err == nil || cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != 19 {
		t.Fatalf("opaque wait lost status: %v", err)
	}
	// Create an orphan through a managed shell and verify this process reaps it.
	shell := exec.Command("/bin/sh", "-c", "sleep 0.05 >/dev/null 2>&1 & echo $!")
	var output strings.Builder
	shell.Stdout = &output
	done, err := defaultReaper.startManaged(shell)
	if err != nil {
		t.Fatal(err)
	}
	err = shell.Wait()
	done()
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(output.String()))
	if err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(5 * time.Second)
	for {
		if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); os.IsNotExist(err) {
			break
		}
		select {
		case <-ticker.C:
		case <-deadline:
			t.Fatalf("orphan %d not reaped", pid)
		}
	}
	// A failed start must not leave a locked registry or stale owner.
	if release, err := defaultReaper.startManaged(exec.Command("/no/such/file")); err == nil || release != nil {
		t.Fatal("bad start accepted")
	}
}
