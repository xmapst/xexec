//go:build !windows

package xexec

import (
	"log/slog"
	"os/exec"
	"runtime/debug"
)

// initCmd creates a command in an independent process group (PGID=PID).
// Linux and FreeBSD retain SIGTERM parent-thread-death behavior for commands.
// Cancellation uses KillAll to signal the entire group and report errors.
func (e *XCmd) initCmd() {
	e.Cmd = exec.CommandContext(e.ctx, e.command, e.args...)
	e.Cmd.SysProcAttr = processSysProcAttr()
	e.Cmd.Cancel = func() error {
		// Prevent cancellation callback panics from escaping.
		defer func() {
			if r := recover(); r != nil {
				slog.Error("recovered command cancellation panic", slog.String("command", e.command), slog.Any("recover", r), slog.String("stack", string(debug.Stack())))
			}
		}()
		if e.Cmd.Process == nil {
			return errProcessNotStarted
		}

		return KillAll(int32(e.Cmd.Process.Pid))
	}
}
