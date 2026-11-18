package xexec

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	// digitRe matches consecutive digits to extract a number from the exit-code file.
	digitRe = regexp.MustCompile(`\d+`)
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

// execPythonScript executes the target Python file through a Python wrapper.
//
// Reasons for using runpy.run_path rather than direct execution:
//   - Set the __main__ context so that if __name__ == '__main__' works.
//   - Forward sys.argv correctly (script path followed by user arguments).
//   - Catch SystemExit and write the exit code to a file.
//   - Use -u to disable Python output buffering for real-time output.
func (s *script) execPythonScript() (code int, err error) {
	// Pass the target path through XEXEC_TARGET rather than interpolating it into script text,
	// avoiding Python raw-string syntax errors for paths containing quotes or backslashes.
	wrapperContent := `# -*- coding: utf-8 -*-
import sys
import os
import runpy

exit_code = 0
script_path = os.environ['XEXEC_TARGET']

# Set sys.argv: the script path first, followed by the supplied arguments.
sys.argv = [script_path] + sys.argv[1:]

try:
    runpy.run_path(script_path, run_name='__main__')
except SystemExit as e:
    if e.code is None:
        exit_code = 0
    elif isinstance(e.code, int):
        exit_code = e.code
    else:
        # sys.exit("message"): print the message to stderr and normalize the exit code to 1.
        print(e.code, file=sys.stderr)
        exit_code = 1
except Exception:
    import traceback
    traceback.print_exc()
    exit_code = 255
finally:
    code_file = os.environ.get('XEXEC_EXIT_CODE_FILE')
    if code_file and not os.path.exists(code_file):
        try:
            with open(code_file, 'w') as f:
                f.write(str(exit_code if isinstance(exit_code, int) else 1))
        except:
            pass
    sys.exit(exit_code if isinstance(exit_code, int) else 1)
`
	wrapperPath := filepath.Join(os.TempDir(), s.randomFilename("wrapper", ".py"))
	defer func() {
		_ = os.Remove(wrapperPath)
	}()
	if err = os.WriteFile(wrapperPath, []byte(wrapperContent), os.ModePerm); err != nil {
		return 255, err
	}
	// Prefer python3, since many modern Linux/macOS systems no longer provide python;
	// fall back to python, then the Windows py launcher.
	python := "python3"
	for _, candidate := range []string{"python3", "python", "py"} {
		if _, lookErr := exec.LookPath(candidate); lookErr == nil {
			python = candidate
			break
		}
	}
	args := append([]string{"-u", wrapperPath}, s.args...)
	return s.exec(python, args...)
}

// exec runs a command, consuming stdout/stderr concurrently and logging output in real time.
//
// Two goroutines read stdout and stderr; a WaitGroup ensures both finish before returning,
// preventing a full output pipe from blocking the child while cmd.Wait() waits.
// startManaged registers wait ownership, so the internal reaper cannot steal the child's exit status.
func (s *script) exec(command string, args ...string) (code int, err error) {
	s.cmd = exec.CommandContext(s.ctx, command, args...)
	s.cmd.Env = s.mergeEnv()
	if s.dir != "" {
		s.cmd.Dir = s.dir
	}
	s.beforeExec() // Configure platform-specific process attributes (process group, hidden window, etc.).
	if s.stdin != nil {
		s.cmd.Stdin = s.stdin
	}
	stdout, err := s.cmd.StdoutPipe()
	if err != nil {
		return 255, err
	}
	stderr, err := s.cmd.StderrPipe()
	if err != nil {
		_ = stdout.Close()
		return 255, err
	}
	release, err := defaultReaper.startManaged(s.cmd)
	if err != nil {
		_ = stdout.Close()
		_ = stderr.Close()
		return 255, err
	}
	defer release() // Keep wait ownership registered until Wait has completed.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		s.consoleOutput("STDOUT", stdout)
	}()
	go func() {
		defer wg.Done()
		s.consoleOutput("STDERR", stderr)
	}()
	err = s.cmd.Wait()
	wg.Wait() // Wait for output readers to finish.

	if err != nil {
		code = 255
	}
	if s.cmd.ProcessState != nil {
		code = s.cmd.ProcessState.ExitCode()
	}
	return code, err
}

// parseExitCodeFromFile reads and parses the exit code from its file.
//
// The wrapper writes the script's exit code before exiting,
// preserving script-level exit values independently of ProcessState.ExitCode().
func (s *script) parseExitCodeFromFile() (int, error) {
	data, err := os.ReadFile(s.exitCodePath)
	if err != nil {
		return 255, &Error{Op: "read code file", Path: s.exitCodePath, Err: err}
	}

	matches := digitRe.FindString(string(bytes.TrimSpace(data)))
	if matches == "" {
		return 255, &Error{Op: "no valid exit code found", Path: s.exitCodePath, Value: string(data)}
	}

	return strconv.Atoi(matches)
}

// mergeEnv merges system and custom environment variables; custom values take precedence.
//
// First collect system variables in a map, then override matching keys with custom values.
// Finally append the built-in target path, exit-code file, and exported-environment file variables
// so the wrapper can locate the script and write its exit code and exported variables.
func (s *script) mergeEnv() []string {
	envMap := make(map[string]string)

	// Load system environment variables as the baseline.
	for _, env := range syscall.Environ() {
		key := s.getEnvKey(env)
		if key != "" {
			envMap[key] = env
		}
	}

	// Custom values override system variables with the same name.
	for _, env := range s.env {
		key := s.getEnvKey(env)
		if key != "" {
			envMap[key] = env
		}
	}

	result := make([]string, 0, len(envMap))
	for _, env := range envMap {
		result = append(result, env)
	}

	// Inject built-in variables:
	//   - XEXEC_TARGET: the absolute target path, read by the wrapper from the environment,
	//     avoiding escaping and injection issues from interpolating paths into script text.
	//     Quotes, spaces, backslashes, and Chinese characters in paths are safe.
	//   - XEXEC_EXIT_CODE_FILE / XEXEC_SAVE_ENV_FILE: the script uses these variables
	//     to locate the files for its exit code and exported environment variables.
	result = append(
		result,
		fmt.Sprintf("XEXEC_TARGET=%s", s.path),
		fmt.Sprintf("XEXEC_EXIT_CODE_FILE=%s", s.exitCodePath),
		fmt.Sprintf("XEXEC_SAVE_ENV_FILE=%s", s.saveEnvPath),
	)

	return result
}

// getEnvKey extracts the key from a KEY=VALUE environment string.
func (s *script) getEnvKey(env string) string {
	key, _, found := strings.Cut(env, "=")
	if !found {
		return ""
	}
	return key
}

// parseSaveEnvFile parses the environment file exported through XEXEC_SAVE_ENV_FILE.
//
// Two file formats are supported:
//   - Single line: KEY=VALUE
//   - Multiline: KEY<<DELIMITER\n...\nDELIMITER (like a Bash here-document)
//
// Safety: scripts cannot override existing system environment variables (checked with os.LookupEnv),
// preventing changes to process-level variables such as PATH and HOME through this mechanism.
func (s *script) parseSaveEnvFile() map[string]any {
	var saveEnv = make(map[string]any)
	file, err := os.Open(s.saveEnvPath)
	if err != nil {
		return saveEnv
	}
	defer func() {
		_ = file.Close()
	}()

	scanner := bufio.NewScanner(file)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 10*1024*1024) // Allow lines up to 10 MB for large JSON values.

	firstLine := true
	for scanner.Scan() {
		line := scanner.Text()

		// Handle UTF-8 BOMs inserted at the beginning of files by some Windows editors.
		if firstLine {
			firstLine = false
			if len(line) >= 3 && line[0] == 0xEF && line[1] == 0xBB && line[2] == 0xBF {
				s.logger.Debugf("[SYSTEM] UTF-8 BOM detected and removed")
				line = line[3:]
			}
		}

		if strings.TrimSpace(line) == "" {
			continue
		}

		// Choose single-line or multiline parsing based on the relative positions of << and =.
		// Case A: only = (no <<) -> single line.
		// Case B: = before << -> single line (<< in the value is ordinary text).
		// Case C: << before = (or no =) -> multiline here-document.
		multiKey, multiRest, hasMulti := strings.Cut(line, "<<")
		singleKey, singleValue, hasSingle := strings.Cut(line, "=")

		isMultiLine := hasMulti && (!hasSingle || len(multiKey) < len(singleKey))

		if isMultiLine {
			key := strings.TrimSpace(multiKey)
			delimiter := strings.TrimSpace(multiRest)

			if key == "" || delimiter == "" {
				s.logger.Errorf("[SYSTEM] invalid multiline format: key or delimiter is empty in line '%s'", line)
				continue
			}

			var valueBuilder strings.Builder
			delimiterFound := false

			for scanner.Scan() {
				content := scanner.Text()
				if strings.TrimSpace(content) == delimiter {
					delimiterFound = true
					break
				}
				if valueBuilder.Len() > 0 {
					valueBuilder.WriteString("\n")
				}
				valueBuilder.WriteString(content)
			}

			if !delimiterFound {
				s.logger.Errorf("[SYSTEM] EOF reached before finding delimiter '%s'", delimiter)
				return saveEnv
			}

			// Disallow overwriting system environment variables to protect the process environment.
			if _, exists := os.LookupEnv(key); exists {
				s.logger.Warnf("[SYSTEM] ignored attempt to overwrite system variable: %s", key)
				continue
			}
			saveEnv[key] = valueBuilder.String()
			s.logger.Debugf("[SYSTEM] parsed env: %s", key)

		} else if hasSingle {
			key := strings.TrimSpace(singleKey)

			if key == "" {
				s.logger.Errorf("[SYSTEM] invalid format: empty key in line '%s'", line)
				continue
			}

			if _, exists := os.LookupEnv(key); exists {
				s.logger.Warnf("[SYSTEM] ignored attempt to overwrite system variable: %s", key)
				continue
			}
			saveEnv[key] = singleValue
			s.logger.Debugf("[SYSTEM] parsed env: %s", key)

		} else {
			s.logger.Errorf("[SYSTEM] invalid format '%v', expected 'KEY=VALUE' or 'KEY<<EOF'", line)
			continue
		}
	}

	if err = scanner.Err(); err != nil {
		s.logger.Errorf("[SYSTEM] error reading file: %v", err)
		return saveEnv
	}

	s.logger.Infof("[SYSTEM] successfully parsed %d environment variables.", len(saveEnv))
	return saveEnv
}
