package xexec

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// script holds all the context for a single script execution.
//
// Execution parameters are configured through functional options (ScriptOption).
// Supported script types include Python, Shell, PowerShell, and Batch.
type script struct {
	cmd          *exec.Cmd          // Underlying system command
	ctx          context.Context    // Execution context for timeouts and cancellation
	cancel       context.CancelFunc // Cancel function called after completion or timeout
	env          []string           // Custom environment variables in KEY=VALUE format
	args         []string           // Command-line arguments passed to the script
	dir          string             // Script working directory
	path         string             // Absolute script path
	stdin        io.Reader          // Standard input; nil means no input
	exitCodePath string             // Exit-code file path, passed to the wrapper through XEXEC_EXIT_CODE_FILE
	saveEnvPath  string             // Exported environment file path, written by the script and read by Go
	logger       ILogger            // Logger
	secrets      []string           // Sensitive strings to redact
	masker       *strings.Replacer  // String replacer built from secrets for log redaction
}

// ExecScriptContent writes script content to a temporary file, executes it, and removes it afterward.
//
// Useful for dynamically generated scripts without requiring callers to manage temporary files.
// Temporary filenames include a nanosecond timestamp to avoid collisions during concurrent execution.
func ExecScriptContent(ctx context.Context, kind, content string, options ...ScriptOption) (code int, env map[string]any, err error) {
	scriptPath := filepath.Join(os.TempDir(), fmt.Sprintf(".script_%d.%s", time.Now().UnixNano(), kind))
	defer func() {
		_ = os.Remove(scriptPath)
	}()
	err = os.WriteFile(scriptPath, []byte(content), os.ModePerm)
	if err != nil {
		return 255, nil, err
	}
	return ExecScript(ctx, scriptPath, options...)
}

// ExecScript executes a script file and returns its exit code, exported environment variables, and error.
//
// Execution flow:
//  1. Create a script instance and apply all options.
//  2. Build the redaction replacer.
//  3. Select the interpreter by extension (.py -> Python; otherwise -> system shell).
//  4. Read the script exit code from the file written by the wrapper.
//  5. Parse the environment variables exported by the script.
//
// The wrapper writes the exit code to the file specified by XEXEC_EXIT_CODE_FILE,
// allowing script-level exit codes to be preserved in cases such as set -e.
func ExecScript(ctx context.Context, path string, options ...ScriptOption) (code int, env map[string]any, err error) {
	if ctx == nil {
		ctx = context.Background()
	}

	s := &script{
		path:   path,
		logger: &defaultLogger{},
	}
	s.exitCodePath = filepath.Join(os.TempDir(), s.randomFilename("exitCode", ".txt"))
	s.saveEnvPath = filepath.Join(os.TempDir(), s.randomFilename("saveEnv", ".txt"))
	s.abs()
	_ = os.Chmod(path, os.ModePerm) // Ensure the script is executable.
	_, err = os.Stat(path)
	if err != nil {
		return 255, nil, &Error{Op: "stat script", Path: path, Err: err}
	}
	s.ctx, s.cancel = context.WithCancel(ctx)
	defer func() {
		s.cancel()
		_ = os.Remove(s.exitCodePath)
		_ = os.Remove(s.saveEnvPath)
	}()
	for _, option := range options {
		option(s)
	}

	// Build replacement pairs [secret, "***", ...] from the secrets list.
	replacements := make([]string, 0, len(s.secrets)*2)
	for _, secret := range s.secrets {
		replacements = append(replacements, secret, "***")
	}
	if len(replacements) > 0 {
		s.masker = strings.NewReplacer(replacements...)
	}

	defer func() {
		// Prefer context errors on cancellation or timeout over script errors.
		if s.ctx.Err() != nil {
			err = s.ctx.Err()
		}
	}()
	code, err = s.execScript()
	// Prefer the wrapper's exit-code file; retain the ProcessState value if parsing fails.
	code1, err1 := s.parseExitCodeFromFile()
	if err1 == nil {
		code = code1
	}
	env = s.parseSaveEnvFile()
	return code, env, err
}

// abs converts the script path to an absolute path.
func (s *script) abs() {
	if filepath.IsAbs(s.path) {
		s.path = filepath.Clean(s.path)
	}
	s.path = filepath.Join(s.dir, s.path)
}

// execScript dispatches execution based on the file extension.
func (s *script) execScript() (code int, err error) {
	ext := filepath.Ext(s.path)
	switch ext {
	case ".py", ".py3":
		return s.execPythonScript()
	default:
		return s.execSysScript()
	}
}

// randomFilename includes a nanosecond timestamp to avoid concurrent temporary filename collisions.
func (s *script) randomFilename(prefix, ext string) string {
	return fmt.Sprintf("%s-%d%s", prefix, time.Now().UnixNano(), ext)
}
