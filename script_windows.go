//go:build windows

package xexec

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

var (
	// acp caches the system's active code page, read once at package initialization.
	// 936 = GBK/GB2312, the default code page on Chinese Windows systems.
	acp = windows.GetACP()
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

// transform handles character encoding conversion on Windows.
//
// If the system code page is GBK (936), or output appears to contain GBK characters,
// convert it to UTF-8 to avoid garbled Chinese logs.
func (s *script) transform(line string) string {
	if acp == 936 || s.isGBK(line) {
		return s.gbkToUtf8(line)
	}
	return line
}

// gbkToUtf8 converts a GBK-encoded string to UTF-8.
//
// Use simplifiedchinese.GBK from golang.org/x/text
// with transform.NewReader for streaming decoding rather than allocating it all at once.
func (s *script) gbkToUtf8(line string) string {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Errorf("[SYSTEM] gbkToUtf8 panic: %v\n%s", r, string(debug.Stack()))
		}
	}()

	reader := transform.NewReader(strings.NewReader(line), simplifiedchinese.GBK.NewDecoder())
	b, err := io.ReadAll(reader)
	if err != nil {
		s.logger.Errorf("[SYSTEM] gbkToUtf8 error: %v", err)
		return line
	}
	return string(b)
}

// isGBK detects whether a string contains GBK-encoded multibyte characters.
//
// GBK double-byte encoding rules:
//   - First byte: 0x81 through 0xFE.
//   - Second byte: 0x40 through 0xFE, excluding 0x7F.
//
// Algorithm:
//   - Skip ASCII characters (<= 0x7F), which do not affect detection.
//   - Require at least one valid GBK double-byte sequence to avoid matching pure ASCII.
//   - Return false immediately for bytes that violate GBK rules.
func (s *script) isGBK(data string) bool {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Errorf("[SYSTEM] isGBK panic: %v\n%s", r, string(debug.Stack()))
		}
	}()

	length := len(data)
	hasMultiByte := false

	for i := 0; i < length; {
		b := data[i]

		if b <= 0x7f {
			i++
			continue
		}

		// Bounds check: a GBK double-byte character requires two bytes.
		if i+1 >= length {
			return false
		}

		b2 := data[i+1]
		// Validate the GBK double-byte character.
		if b >= 0x81 && b <= 0xfe && b2 >= 0x40 && b2 <= 0xfe && b2 != 0x7f {
			hasMultiByte = true
			i += 2
			continue
		}

		// Invalid GBK byte sequence.
		return false
	}

	// Do not treat pure ASCII as GBK, avoiding unnecessary conversion.
	return hasMultiByte
}
