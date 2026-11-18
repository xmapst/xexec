package xexec

import (
	"os/exec"
	"sync"
)

// childReaper owns command wait registration and platform-specific orphan reaping.
// One mutex serializes command starts, ownership changes, and orphan scans.
type childReaper struct {
	once    sync.Once
	mu      sync.Mutex
	managed map[int]*exec.Cmd
	opaque  int
	changed chan struct{}
}

var defaultReaper = &childReaper{
	managed: make(map[int]*exec.Cmd),
	changed: make(chan struct{}, 1),
}

func (r *childReaper) notifyChange() {
	select {
	case r.changed <- struct{}{}:
	default:
	}
}

// startManaged atomically starts a command and registers its wait owner.
// The caller must call Cmd.Wait before invoking the idempotent release function.
func (r *childReaper) startManaged(cmd *exec.Cmd) (release func(), err error) {
	r.ensureStarted()
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	pid := cmd.Process.Pid
	r.managed[pid] = cmd
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			if r.managed[pid] == cmd {
				delete(r.managed, pid)
			}
			r.mu.Unlock()
			r.notifyChange()
		})
	}, nil
}

// beginOpaqueWait protects internal synchronous executors without PID hooks.
// Acquire before spawning and release only after all internal waits complete.
// Orphan scans pause during this interval, but managed starts and waits do not.
func (r *childReaper) beginOpaqueWait() func() {
	r.ensureStarted()
	r.mu.Lock()
	r.opaque++
	r.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			r.opaque--
			r.mu.Unlock()
			r.notifyChange()
		})
	}
}

// withUnowned runs a scan with stable ownership; active opaque waits skip it.
// The callback must exclude owned PIDs and only perform nonblocking waits.
// It must not call back into this package, spawn processes, block on live
// children, or retain the owned closure beyond its invocation.
func (r *childReaper) withUnowned(scan func(owned func(int) bool) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.opaque > 0 {
		return nil
	}
	return scan(func(pid int) bool { _, ok := r.managed[pid]; return ok })
}
