//go:build linux

package xexec

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// childPIDs merges every thread child list; Go may spawn children on any OS thread.
func (r *childReaper) childPIDs() (map[int]struct{}, error) {
	tasks, err := os.ReadDir("/proc/self/task")
	if err != nil {
		return nil, err
	}
	pids := make(map[int]struct{})
	for _, task := range tasks {
		data, err := os.ReadFile(filepath.Join("/proc/self/task", task.Name(), "children"))
		if os.IsNotExist(err) {
			continue
		} // The thread exited; a later scan covers reparented children.
		if err != nil {
			return nil, err
		}
		for field := range strings.FieldsSeq(string(data)) {
			pid, err := strconv.Atoi(field)
			if err != nil || pid <= 0 {
				return nil, &Error{Op: "invalid child PID", Value: field, Err: err}
			}
			pids[pid] = struct{}{}
		}
	}
	return pids, nil
}

func (r *childReaper) zombieChild(data string, parent int) bool {
	// The comm field may contain spaces and parentheses; parse after the last closing parenthesis.
	end := strings.LastIndexByte(data, ')')
	if end < 0 {
		return false
	}
	fields := strings.Fields(data[end+1:])
	if len(fields) < 2 || fields[0] != "Z" {
		return false
	}
	ppid, err := strconv.Atoi(fields[1])
	return err == nil && ppid == parent
}

// scanChildren only waits for unowned zombie direct children, never wait4(-1).
// Scans share the start/registration lock; Cmd.Wait runs unlocked and its PID stays excluded.
func (r *childReaper) scanChildren() error {
	return r.withUnowned(func(owned func(int) bool) error {
		pids, err := r.childPIDs()
		if err != nil {
			return err
		}
		for pid := range pids {
			if owned(pid) {
				continue
			}
			data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return err
			}
			if !r.zombieChild(string(data), os.Getpid()) {
				continue
			}
			var ws unix.WaitStatus
			for {
				_, err = unix.Wait4(pid, &ws, unix.WNOHANG, nil)
				if !errors.Is(err, unix.EINTR) {
					break
				}
			}
			if errors.Is(err, unix.ECHILD) {
				continue
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *childReaper) serve(signals chan os.Signal) {
	defer signal.Stop(signals)
	// Periodic scans cover procfs snapshot changes after thread exit or orphan adoption.
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if err := r.scanChildren(); err != nil {
			slog.Error("reaper scan failed; will retry", "err", err)
		}
		select {
		case <-signals:
		case <-r.changed:
		case <-ticker.C:
		}
	}
}

// ensureStarted starts lazily before execution and has no effect outside PID 1.
func (r *childReaper) ensureStarted() {
	if os.Getpid() == 1 {
		r.start()
	}
}

// start is internal and starts at most one reaper per process.
func (r *childReaper) start() {
	r.once.Do(func() {
		if err := enableChildSubReaper(); err != nil {
			slog.Warn("failed to enable child subreaper", "err", err)
		}
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, unix.SIGCHLD)
		go r.serve(signals)
	})
}
