//go:build windows

package xexec

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWindowsBatchScripts(t *testing.T) {
	for _, ext := range []string{"bat", "cmd"} {
		t.Run(ext, func(t *testing.T) {
			for _, code := range []int{0, 23} {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				content := "@echo off\r\nexit /b 0\r\n"
				if code != 0 {
					content = "@echo off\r\nexit /b 23\r\n"
				}
				got, _, err := ExecScriptContent(ctx, ext, content, WithScriptLogger(&captureTestLogger{}))
				cancel()
				if got != code || (err != nil) != (code != 0) {
					t.Fatalf("exit %d: code=%d err=%v", code, got, err)
				}
				if code != 0 {
					var exitErr *exec.ExitError
					if !errors.As(err, &exitErr) {
						t.Fatalf("missing process exit error: %v", err)
					}
				}
			}
		})
	}
}

func TestWindowsBatchOptionsAndExportedEnvironment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target with space's.bat")
	content := "@echo off\r\n" +
		"if not \"%~1\"==\"hello world\" exit /b 31\r\n" +
		"if not \"%~2\"==\"second\" exit /b 32\r\n" +
		"if not \"%XEXEC_BATCH_INPUT%\"==\"custom\" exit /b 33\r\n" +
		"set /p received=\r\n" +
		"if not \"%received%\"==\"input data\" exit /b 34\r\n" +
		">\"%XEXEC_SAVE_ENV_FILE%\" echo XEXEC_BATCH_RESULT=ok\r\n" +
		">>\"%XEXEC_SAVE_ENV_FILE%\" echo XEXEC_BATCH_CWD=%CD%\r\n" +
		"exit /b 0\r\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	code, env, err := ExecScript(ctx, path, WithScriptWorkdir(dir), WithScriptArgs("hello world", "second"),
		WithScriptEnv("XEXEC_BATCH_INPUT=custom"), WithScriptStdin(strings.NewReader("input data\r\n")), WithScriptLogger(&captureTestLogger{}))
	if err != nil || code != 0 {
		t.Fatalf("batch options: code=%d err=%v", code, err)
	}
	cwd, _ := env["XEXEC_BATCH_CWD"].(string)
	if env["XEXEC_BATCH_RESULT"] != "ok" {
		t.Fatalf("exported batch environment: %#v", env)
	}
	assertTestWorkingDirectory(t, cwd, dir)
}

func TestWindowsPowerShellScripts(t *testing.T) {
	if _, err := exec.LookPath("powershell.exe"); err != nil {
		t.Skip("Windows PowerShell is not installed")
	}
	for _, tc := range []struct {
		name, ext, content string
		code               int
	}{
		{"normal", "ps1", "Write-Output 'ok'", 0},
		{"explicit-zero", "ps1", "exit 0", 0},
		{"explicit-nonzero", "ps1", "exit 7", 7},
		{"caught-exception", "ps1", "throw 'expected failure'", 255},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			code, _, err := ExecScriptContent(ctx, tc.ext, tc.content, WithScriptLogger(&captureTestLogger{}))
			if code != tc.code || (err != nil) != (tc.code != 0) {
				t.Fatalf("PowerShell: code=%d err=%v, want %d", code, err, tc.code)
			}
		})
	}
}

func TestWindowsPowerShellOptionsAndExportedEnvironment(t *testing.T) {
	if _, err := exec.LookPath("powershell.exe"); err != nil {
		t.Skip("Windows PowerShell is not installed")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "target with space's.ps1")
	content := `if ($args.Count -ne 2 -or $args[0] -ne 'hello world' -or $args[1] -ne 'second') { exit 31 }
if ($env:XEXEC_PS_INPUT -ne 'custom') { exit 32 }
if ([Console]::ReadLine() -ne 'input data') { exit 33 }
@('XEXEC_PS_RESULT=ok', ('XEXEC_PS_CWD=' + (Get-Location).Path)) | Set-Content -LiteralPath $env:XEXEC_SAVE_ENV_FILE -Encoding UTF8
exit 0
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	code, env, err := ExecScript(ctx, path, WithScriptWorkdir(dir), WithScriptArgs("hello world", "second"),
		WithScriptEnv("XEXEC_PS_INPUT=custom"), WithScriptStdin(strings.NewReader("input data\r\n")), WithScriptLogger(&captureTestLogger{}))
	if code != 0 || err != nil {
		t.Fatalf("PowerShell options: code=%d err=%v", code, err)
	}
	cwd, _ := env["XEXEC_PS_CWD"].(string)
	if env["XEXEC_PS_RESULT"] != "ok" {
		t.Fatalf("exported PowerShell environment: %#v", env)
	}
	assertTestWorkingDirectory(t, cwd, dir)
}

func TestWindowsWrapperCreationFailure(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	t.Setenv("TMP", dir)
	t.Setenv("TEMP", dir)
	for _, ext := range []string{".bat", ".ps1"} {
		s := &script{path: "target" + ext}
		if code, err := s.execSysScript(); code != 255 || err == nil {
			t.Fatalf("%s wrapper: code=%d err=%v", ext, code, err)
		}
	}
}
