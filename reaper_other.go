//go:build !linux

package xexec

// On other platforms, callers wait for direct children; no orphan reaper is started.
func (r *childReaper) ensureStarted() {}
