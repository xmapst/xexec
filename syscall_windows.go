//go:build windows

package xexec

import (
	"errors"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// KillAll attempts to terminate a Windows process tree using native APIs.
// Depth-first traversal retains only the active ancestor chain of handles;
// descendants are terminated and closed before visiting the next sibling.
// A single snapshot is best-effort: newly spawned descendants may escape. PID/parent
// relationships and creation times cannot provide the guarantees of a Windows Job Object.
// Non-positive PIDs and PID 4 are rejected before calling native APIs.
func KillAll(pid int32) error {
	return killWindowsTree(pid, windowsTreeOps{
		open:     openWindowsTreeProcess,
		snapshot: processTreeSnapshot,
		cutoff: func() uint64 {
			return filetimeValue(windows.NsecToFiletime(time.Now().UnixNano()))
		},
	})
}

type windowsTreeProcess struct {
	created   uint64
	terminate func() error
	close     func()
}

type windowsTreeOps struct {
	open     func(uint32) (*windowsTreeProcess, error)
	snapshot func() (map[uint32][]uint32, error)
	cutoff   func() uint64
}

func filetimeValue(t windows.Filetime) uint64 {
	return uint64(t.HighDateTime)<<32 | uint64(t.LowDateTime)
}

func openWindowsTreeProcess(pid uint32) (*windowsTreeProcess, error) {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return nil, err
	}
	var created, exited, kernel, user windows.Filetime
	if err = windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		_ = windows.CloseHandle(h)
		return nil, err
	}
	return &windowsTreeProcess{
		created: filetimeValue(created),
		close:   func() { _ = windows.CloseHandle(h) },
		terminate: func() error {
			// A parent can exit naturally after its descendants are terminated.
			state, err := windows.WaitForSingleObject(h, 0)
			if err != nil {
				return err
			}
			if state == windows.WAIT_OBJECT_0 {
				return nil
			}
			if err = windows.TerminateProcess(h, 1); err != nil {
				if state, waitErr := windows.WaitForSingleObject(h, 0); waitErr == nil && state == windows.WAIT_OBJECT_0 {
					return nil
				}
				return err
			}
			return nil
		},
	}, nil
}

// processTreeSnapshot records PID relationships only; these are not ownership proofs.
func processTreeSnapshot() (map[uint32][]uint32, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, &Error{Op: "snapshot processes", Err: err}
	}
	defer windows.CloseHandle(snapshot)
	children := make(map[uint32][]uint32)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	err = windows.Process32First(snapshot, &entry)
	for err == nil {
		children[entry.ParentProcessID] = append(children[entry.ParentProcessID], entry.ProcessID)
		err = windows.Process32Next(snapshot, &entry)
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return nil, &Error{Op: "enumerate processes", Err: err}
	}
	return children, nil
}

func killWindowsTree(rootPID int32, ops windowsTreeOps) error {
	if rootPID <= 0 || rootPID == 4 {
		return &Error{Op: "kill all: invalid pid", PID: int64(rootPID)}
	}
	pid := uint32(rootPID)
	// Hold the root before taking the snapshot so its PID cannot be reused midway.
	root, err := ops.open(pid)
	if err != nil {
		return &Error{Op: "open root process", PID: int64(pid), Err: err}
	}
	cutoff := ops.cutoff()
	children, scanErr := ops.snapshot()
	var errs error
	if scanErr != nil {
		errs = &Error{Op: "snapshot process tree", PID: int64(pid), Err: scanErr}
	}
	visited := map[uint32]bool{pid: true, 0: true, 4: true}
	// Each invocation owns one handle. Keep ancestors alive while visiting their
	// descendants, but close completed branches before opening the next sibling.
	// This bounds simultaneously retained handles by tree depth, not tree size.
	var visit func(uint32, *windowsTreeProcess)
	visit = func(pid uint32, process *windowsTreeProcess) {
		defer process.close()
		for _, childPID := range children[pid] {
			if visited[childPID] {
				continue
			}
			visited[childPID] = true
			child, err := ops.open(childPID)
			if err != nil {
				errs = errors.Join(errs, &Error{Op: "open descendant process", PID: int64(childPID), Err: err})
				continue
			}
			// Reject stale parent IDs and PIDs reused after snapshot acquisition began.
			if child.created < process.created || child.created > cutoff {
				child.close()
				errs = errors.Join(errs, &Error{Op: "process identity changed or parent relationship is stale", PID: int64(childPID)})
				continue
			}
			visit(childPID, child)
		}
		if err := process.terminate(); err != nil {
			errs = errors.Join(errs, &Error{Op: "terminate process", PID: int64(pid), Err: err})
		}
	}
	visit(pid, root)
	return errs
}
