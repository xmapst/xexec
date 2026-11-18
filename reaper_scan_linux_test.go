//go:build linux

package xexec

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func waitForTestZombie(t *testing.T, r *childReaper, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err == nil && r.zombieChild(string(data), os.Getpid()) {
			return
		}
		<-time.After(10 * time.Millisecond)
	}
	t.Fatalf("child %d did not become a zombie", pid)
}

func TestReaperScanPreservesOwnedAndLiveChildren(t *testing.T) {
	if os.Getpid() == 1 {
		t.Skip("isolated scanner tests run outside PID 1; Docker has dedicated integration tests")
	}
	r := &childReaper{managed: make(map[int]*exec.Cmd), changed: make(chan struct{}, 1)}
	owned := exec.Command("/bin/sh", "-c", "exit 17")
	release, err := r.startManaged(owned)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	defer owned.Wait()
	waitForTestZombie(t, r, owned.Process.Pid)
	live := exec.Command("/bin/sh", "-c", "sleep 10")
	live.SysProcAttr = sysProcAttr()
	if err := live.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { KillAll(int32(live.Process.Pid)); live.Wait() }()
	pids, err := r.childPIDs()
	if err != nil {
		t.Fatal(err)
	}
	for _, pid := range []int{owned.Process.Pid, live.Process.Pid} {
		if _, ok := pids[pid]; !ok {
			t.Errorf("child %d missing from procfs", pid)
		}
	}
	if err := r.scanChildren(); err != nil {
		t.Fatal(err)
	}
	if err := live.Process.Signal(unix.Signal(0)); err != nil {
		t.Fatalf("scanner killed live child: %v", err)
	}
	err = owned.Wait()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 17 {
		t.Fatalf("owned exit status lost: %v", err)
	}
}

func TestReaperScanCollectsOnlyUnownedZombie(t *testing.T) {
	if os.Getpid() == 1 {
		t.Skip("isolated scanner tests run outside PID 1")
	}
	r := &childReaper{managed: make(map[int]*exec.Cmd), changed: make(chan struct{}, 1)}
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Wait()
	waitForTestZombie(t, r, cmd.Process.Pid)
	if err := r.scanChildren(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fmt.Sprintf("/proc/%d", cmd.Process.Pid)); !os.IsNotExist(err) {
		t.Fatalf("unowned zombie remains: %v", err)
	}
	if err := cmd.Wait(); !errors.Is(err, unix.ECHILD) {
		t.Fatalf("scanner did not consume unowned status: %v", err)
	}
}
