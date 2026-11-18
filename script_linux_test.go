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
)

func TestScriptDoesNotPauseReaping(t *testing.T) {
	if os.Getenv("XSCRIPT_LIVE_REAPER_HELPER") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestScriptDoesNotPauseReaping$")
		cmd.Env = append(os.Environ(), "XSCRIPT_LIVE_REAPER_HELPER=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("helper: %v\n%s", err, out)
		}
		return
	}
	if os.Getpid() != 1 {
		defaultReaper.start()
	}
	dir := t.TempDir()
	orphanFile := filepath.Join(dir, "orphan")
	finishFile := filepath.Join(dir, "finish")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	type result struct {
		code int
		err  error
	}
	done := make(chan result, 1)
	go func() {
		code, _, err := ExecScriptContent(ctx, "sh", `#!/bin/sh
sh -c 'sleep 0.05 >/dev/null 2>&1 & echo $!' > "$ORPHAN_PID_FILE"
while [ ! -f "$FINISH_FILE" ]; do sleep 0.05; done
exit 17
`, WithScriptEnv("ORPHAN_PID_FILE="+orphanFile, "FINISH_FILE="+finishFile))
		done <- result{code, err}
	}()
	defer func() { cancel(); <-done }()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var pid int
	for {
		if pid == 0 {
			data, _ := os.ReadFile(orphanFile)
			pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		}
		if pid > 0 {
			if _, err := os.Stat("/proc/" + strconv.Itoa(pid)); os.IsNotExist(err) {
				break
			}
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("orphan was not reaped while the script was running")
		}
	}
	defaultReaper.mu.Lock()
	opaque, owned := defaultReaper.opaque, len(defaultReaper.managed)
	defaultReaper.mu.Unlock()
	if opaque != 0 || owned == 0 {
		t.Fatalf("script ownership: opaque=%d managed=%d", opaque, owned)
	}
	cmd, err := ExecContext(ctx, "/bin/sh", WithExecArgs("-c", "exit 23"))
	if err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != 23 {
		t.Fatal("concurrent managed command lost its exit status")
	}
	if err := os.WriteFile(finishFile, nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		// Put the result back for the deferred cancellation/join.
		done <- got
		if got.code != 17 || got.err == nil {
			t.Fatalf("script result: code=%d err=%v", got.code, got.err)
		}
	case <-ctx.Done():
		t.Fatal("script did not finish")
	}
}

func TestScriptStartFailure(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	defaultReaper.mu.Lock()
	before := len(defaultReaper.managed)
	defaultReaper.mu.Unlock()
	code, _, err := ExecScriptContent(context.Background(), "sh", "#!/bin/sh\nexit 0\n")
	if err == nil || code != 255 {
		t.Fatalf("missing interpreter: code=%d err=%v", code, err)
	}
	defaultReaper.mu.Lock()
	after := len(defaultReaper.managed)
	defaultReaper.mu.Unlock()
	if after != before {
		t.Fatalf("failed script leaked wait ownership: before=%d after=%d", before, after)
	}
}

func TestScriptWithReaper(t *testing.T) {
	if os.Getenv("XSCRIPT_REAPER_HELPER") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestScriptWithReaper$")
		cmd.Env = append(os.Environ(), "XSCRIPT_REAPER_HELPER=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		return
	}
	defaultReaper.start()
	for range 20 {
		code, _, err := ExecScriptContent(context.Background(), "sh", "exit 17")
		var exitErr *exec.ExitError
		if code != 17 || !errors.As(err, &exitErr) || exitErr.ExitCode() != 17 {
			t.Fatalf("lost script exit status %d: %v", code, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, _, err := ExecScriptContent(ctx, "sh", "sleep 10")
	if err == nil {
		t.Fatal("canceled script succeeded")
	}
}
