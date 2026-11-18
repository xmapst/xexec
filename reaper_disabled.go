//go:build windows || darwin

package xexec

// On Windows and macOS, callers wait for direct children;
// no orphan reaper is started.
func (r *childReaper) ensureStarted() {}
