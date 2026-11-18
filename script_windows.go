//go:build windows

package xexec

import (
	"errors"
	"os"
	"path/filepath"
	"runtime/debug"
	"syscall"
)

// execSysScript selects PowerShell or cmd.exe by script extension on Windows.
//
// PowerShell wrapper features:
//   - -NonInteractive prevents waiting for user input.
//   - -ExecutionPolicy Bypass bypasses execution policy restrictions.
//   - Catch exceptions and write the exit-code file; normalize a $null exit code to 0.
//
// cmd.exe wrapper features:
//   - @echo off disables command echoing.
//   - %%* forwards all arguments (cmd syntax).
//   - Write the exit-code file only if it does not already exist.
func (s *script) execSysScript() (code int, err error) {
	ext := filepath.Ext(s.path)
	var (
		command        string
		args           []string
		wrapperPath    string
		wrapperContent string
	)
	switch ext {
	case ".ps1", ".ps":
		command = "powershell.exe"
		args = []string{"-NoLogo", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File"}
		wrapperPath = filepath.Join(os.TempDir(), s.randomFilename("wrapper", ".ps1"))
		// Read the target path from $env:XEXEC_TARGET rather than interpolating script text,
		// avoiding quoting problems with special characters and keeping Chinese paths out of the wrapper.
		// This avoids garbled paths when the UTF-8 wrapper runs under a GBK code page.
		// PowerShell recommends putting $null on the left side of comparisons.
		wrapperContent = `$ErrorActionPreference='Continue'
try {
    & "$env:XEXEC_TARGET" @args
    if ($null -eq $LASTEXITCODE) {
        $exitCode = 0
    } else {
        $exitCode = $LASTEXITCODE
    }
} catch {
    $exitCode = 255
    Write-Error "[$($_.Exception.GetType().FullName)] $($_.Exception.Message)"
} finally {
    if (-not (Test-Path "$env:XEXEC_EXIT_CODE_FILE")) {
        $exitCode | Out-File -FilePath "$env:XEXEC_EXIT_CODE_FILE" -Encoding ASCII
    }
    exit $exitCode
}`
	default:
		command = "cmd.exe"
		args = []string{"/C"}
		wrapperPath = filepath.Join(os.TempDir(), s.randomFilename("wrapper", ".bat"))
		// Read the target path from %XEXEC_TARGET% to avoid interpolation and garbled Chinese paths.
		// Use <nul set /p to write the exit code without echo's trailing spaces.
		wrapperContent = `@echo off
call "%XEXEC_TARGET%" %*
set exitcode=%ERRORLEVEL%
if not exist "%XEXEC_EXIT_CODE_FILE%" (
    <nul set /p=%exitcode%> "%XEXEC_EXIT_CODE_FILE%"
)
exit /b %exitcode%`
	}
	defer func() {
		_ = os.Remove(wrapperPath)
	}()
	if err = os.WriteFile(wrapperPath, []byte(wrapperContent), os.ModePerm); err != nil {
		return 255, err
	}
	args = append(args, wrapperPath)
	args = append(args, s.args...)
	return s.exec(command, args...)
}

// beforeExec isolates console signals and configures native process-tree cancellation.
// If tree cleanup fails, root-only fallback preserves the original cleanup error.
func (s *script) beforeExec() {
	s.beforeExecWithKillAll(KillAll)
}

// Dependency injection keeps cancellation failure tests independent of host permissions.
func (s *script) beforeExecWithKillAll(killAll func(int32) error) {
	s.cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP, // Create an isolated process group.
		HideWindow:    true,                             // Hide the console window.
	}
	s.cmd.Cancel = func() error {
		defer func() {
			if r := recover(); r != nil {
				s.logger.Errorf("[SYSTEM] cmd cancel panic: %v\n%s", r, string(debug.Stack()))
			}
			_ = os.WriteFile(s.exitCodePath, []byte("137"), os.ModePerm)
		}()
		if s.cmd.Process == nil {
			return errProcessNotStarted
		}
		pid := s.cmd.Process.Pid
		killErr := killAll(int32(pid))
		if killErr == nil {
			return nil
		}

		// If native tree cleanup fails, fall back to killing the root directly.
		// Descendants may survive, so return the original cleanup error
		// regardless of whether Kill succeeds.
		errs := error(&Error{Op: "killall", PID: int64(pid), Err: killErr})
		if err := s.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			errs = errors.Join(errs, &Error{Op: "kill process", PID: int64(pid), Err: err})
		}
		return &Error{Op: "error cancelling", PID: int64(pid), Err: errs}
	}
}
