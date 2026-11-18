//go:build linux || darwin

package xexec

import (
	"os"
	"path/filepath"
	"runtime/debug"
)

// execSysScript runs the target script through an sh wrapper on Unix/Linux.
//
// The wrapper:
//  1. Uses traps for EXIT/INT/TERM to write the exit-code file when the shell exits.
//  2. Forwards arguments with "$@", preserving spaces within arguments.
//  3. Writes only if the file does not exist, preserving an exit code already recorded.
//
// Why use a wrapper and trap instead of exec to replace sh with the target:
// The wrapper remains available to record the script-level exit code after the target exits.
// This supports the exit-code file convention without relying solely on ProcessState.
// Execution starts through startManaged, which registers wait ownership,
// and completes with Cmd.Wait. The internal reaper does not collect registered children.
// An unrelated external wait(-1) reaper or SIGCHLD=SIG_IGN is unsupported:
// it can consume or discard the status before Cmd.Wait observes it.
// Any resulting ECHILD error is returned, not suppressed; an exit-code file
// cannot restore the missing wait status or make such external reapers safe.
func (s *script) execSysScript() (code int, err error) {
	_ = os.Chmod(s.path, os.ModePerm)
	// Pass the target path through XEXEC_TARGET rather than interpolating script text,
	// avoiding broken shell quoting or command injection from special characters in paths.
	// Avoid non-POSIX local; use printf rather than echo for consistent behavior.
	wrapperContent := `#!/bin/sh
_save_exit_code() {
	code=$?
	[ ! -f "${XEXEC_EXIT_CODE_FILE}" ] && printf '%s\n' "$code" > "${XEXEC_EXIT_CODE_FILE}"
	exit $code
}
trap _save_exit_code EXIT INT TERM
"${XEXEC_TARGET}" "$@"
`

	wrapperPath := filepath.Join(os.TempDir(), s.randomFilename("wrapper", ".sh"))
	defer func() {
		_ = os.Remove(wrapperPath)
	}()

	if err = os.WriteFile(wrapperPath, []byte(wrapperContent), os.ModePerm); err != nil {
		return 255, &Error{Op: "failed to create wrapper script", Path: wrapperPath, Err: err}
	}

	args := append([]string{wrapperPath}, s.args...)
	return s.exec("sh", args...)
}

// beforeExec configures process attributes on Unix.
//
// A new process group (Setpgid=true) allows:
//   - The child and its descendants to share an isolated process group.
//   - Cancellation to signal the whole group with Kill(-pid, SIGKILL).
//   - Cleanup of descendants that would otherwise remain orphaned.
//
// The cmd.Cancel hook runs on context cancellation and terminates the process tree.
func (s *script) beforeExec() {
	// Build SysProcAttr for the platform (see sysProcAttr):
	//   - All Unix platforms set Setpgid, making the child a new process group leader.
	//     Kill(-pid, SIGKILL) in cancel can then terminate the entire group.
	//   - Linux/FreeBSD also set Pdeathsig=SIGKILL for kernel-driven child cleanup
	//     if Go crashes or is killed before defer cancel can run.
	s.cmd.SysProcAttr = sysProcAttr()

	s.cmd.Cancel = func() error {
		defer func() {
			if r := recover(); r != nil {
				s.logger.Errorf("[SYSTEM] cmd cancel panic: %v\n%s", r, string(debug.Stack()))
			}
			// Write 137 (128 + SIGKILL signal number 9) as the standard killed exit code.
			_ = os.WriteFile(s.exitCodePath, []byte("137"), os.ModePerm)
		}()

		if s.cmd.Process == nil {
			return errProcessNotStarted
		}

		if err := KillAll(int32(s.cmd.Process.Pid)); err != nil {
			return &Error{Op: "error cancelling", PID: int64(s.cmd.Process.Pid), Err: err}
		}
		return nil
	}
}
