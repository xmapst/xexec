package xexec

import (
	"os/exec"
	"sync"
)

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
