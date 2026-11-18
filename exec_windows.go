//go:build windows

package xexec

import (
	"log/slog"
	"os/exec"
	"runtime/debug"

	"golang.org/x/sys/windows"
)

// initCmd executes programs directly, preserving argument boundaries and avoiding
// implicit shell interpretation. Use cmd.exe /C explicitly when shell syntax is needed.
// Cancellation delegates process-tree termination to KillAll.
func (e *XCmd) initCmd() {
	e.Cmd = exec.CommandContext(e.ctx, e.command, e.args...)
	e.Cmd.SysProcAttr = &windows.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP, // Isolate console signals.
		HideWindow:    true,                             // Hide the console window.
	}
	e.Cmd.Cancel = func() error {
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
