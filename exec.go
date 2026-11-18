// Package xexec provides cross-platform command and script execution.
//
// Features:
//   - Context-aware cancellation.
//   - Asynchronous starts with a waitable command handle.
//   - Bounded console log files for stdout and stderr.
//   - Platform-specific process group or process tree termination.
package xexec

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"
)

const defaultConsoleMaxSize int64 = 50 * 1024 * 1024

// Bound output draining after the root exits or its context is canceled.
const defaultOutputWaitDelay = 250 * time.Millisecond

// XCmd holds the configuration and runtime state of a single command.
//
// Platform-specific command construction and termination live in the
// exec_*.go and syscall_*.go files.
type XCmd struct {
	*exec.Cmd

	ctx         context.Context
	output      processOutput
	workingDir  string
	command     string
	args        []string
	extraEnv    []string
	releaseWait func()
}

// CommandOption configures a command before it starts.
type CommandOption func(*XCmd)

// WithExecWorkingDir sets the working directory; empty inherits the parent directory.
func WithExecWorkingDir(workingDir string) CommandOption {
	return func(c *XCmd) {
		c.workingDir = workingDir
	}
}

// WithExecArgs sets the command arguments.
func WithExecArgs(args ...string) CommandOption {
	return func(c *XCmd) {
		c.args = args
	}
}

// WithExecEnv adds environment variables in "KEY=VALUE" format.
//
// Variables are appended to the inherited environment rather than replacing it.
func WithExecEnv(envs ...string) CommandOption {
	return func(c *XCmd) {
		c.extraEnv = envs
	}
}

// ExecContext asynchronously starts a command with the given context and options.
//
// The caller must call Wait on the returned XCmd to release system resources.
// A nil context is replaced with context.Background.
func ExecContext(ctx context.Context, command string, options ...CommandOption) (*XCmd, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil, errEmptyCommand
	}
	if ctx == nil {
		ctx = context.Background()
	}

	cmd := &XCmd{
		ctx:     ctx,
		command: command,
	}
	for _, option := range options {
		if option != nil {
			option(cmd)
		}
	}
	return cmd.start()
}

// start constructs the command, configures its environment and output, and starts it.
func (e *XCmd) start() (*XCmd, error) {
	e.initCmd()
	e.Cmd.WaitDelay = defaultOutputWaitDelay

	e.Cmd.Env = append(os.Environ(), e.extraEnv...)
	if e.workingDir != "" {
		e.Cmd.Dir = e.workingDir
	}

	if err := e.attachConsoleOutput(); err != nil {
		return nil, err
	}
	release, err := defaultReaper.startManaged(e.Cmd)
	if err != nil {
		e.closeConsoleOutput()
		slog.Error("failed to start command", slog.String("command", e.command), slog.Any("args", e.args), slog.Any("err", err))
		return nil, err
	}

	e.releaseWait = release
	return e, nil
}

// Wait waits for exit and closes the console log after stdout/stderr copies finish.
// Output draining is bounded by WaitDelay. If descendants keep output pipes open,
// Wait reports exec.ErrWaitDelay and attempts to terminate the remaining process group.
func (e *XCmd) Wait() error {
	if e.Cmd == nil {
		return errCommandNotInitialized
	}
	defer e.closeConsoleOutput()
	if e.releaseWait != nil {
		defer e.releaseWait()
	}
	err := e.Cmd.Wait()
	if errors.Is(err, exec.ErrWaitDelay) && e.Cmd.Process != nil {
		// The root has exited but descendants retained output handles. On Unix,
		// the group remains addressable without its leader. Windows tree cleanup
		// may fail after root exit; preserve that error rather than claiming success.
		err = errors.Join(err, KillAll(int32(e.Cmd.Process.Pid)))
	}
	if err != nil && e.ctx != nil && e.ctx.Err() != nil {
		err = errors.Join(err, e.ctx.Err())
	}
	return err
}

// attachConsoleOutput binds stdout and stderr to a shared bounded log file.
func (e *XCmd) attachConsoleOutput() error {
	if e.Cmd == nil {
		return errCommandNotInitialized
	}

	output, err := newConsoleFile(e.command, e.args, defaultConsoleMaxSize)
	if err != nil {
		slog.Error("failed to create console log", slog.String("command", e.command), slog.Any("args", e.args), slog.Any("err", err))
		return err
	}

	e.output = output
	e.Cmd.Stdout = output
	e.Cmd.Stderr = output
	return nil
}

// closeConsoleOutput flushes and closes the console log and is safe to repeat.
func (e *XCmd) closeConsoleOutput() {
	if e.output == nil {
		return
	}

	name := e.output.Name()
	if err := e.output.Sync(); err != nil && !errors.Is(err, os.ErrClosed) {
		slog.Error("failed to sync console log", slog.String("name", name), slog.Any("err", err))
	}
	if err := e.output.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		slog.Error("failed to close console log", slog.String("name", name), slog.Any("err", err))
	}
	e.output = nil
}
