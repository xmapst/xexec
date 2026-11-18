//go:build linux

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

func TestLinuxShellScriptArgumentsWorkdirAndEnvironment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "script with ' quotes.sh")
	content := `#!/bin/sh
[ "$1" = 'hello world' ] || exit 21
[ "$2" = '' ] || exit 22
IFS= read -r input
[ "$input" = 'stdin data' ] || exit 23
[ "$XEXEC_SHELL_CUSTOM" = 'custom' ] || exit 24
printf 'XEXEC_SHELL_RESULT=ok\nXEXEC_SHELL_WORKDIR=%s\n' "$PWD" > "$XEXEC_SAVE_ENV_FILE"
exit 0
`
	if err := os.WriteFile(path, []byte(content), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	code, env, err := ExecScript(ctx, path, WithScriptWorkdir(dir), WithScriptArgs("hello world", ""), WithScriptEnv("XEXEC_SHELL_CUSTOM=custom"), WithScriptStdin(strings.NewReader("stdin data\n")), WithScriptLogger(&captureTestLogger{}))
	if code != 0 || err != nil || env["XEXEC_SHELL_RESULT"] != "ok" || env["XEXEC_SHELL_WORKDIR"] != dir {
		t.Fatalf("shell result: code=%d env=%#v err=%v", code, env, err)
	}
}

func TestLinuxShellExitAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		code          int
	}{
		{"normal", "#!/bin/sh\nexit 0\n", 0},
		{"exit", "#!/bin/sh\nexit 23\n", 23},
		{"set-e", "#!/bin/sh\nset -e\nfalse\necho should-not-run\n", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, _, err := ExecScriptContent(nil, "sh", tc.content, WithScriptLogger(&captureTestLogger{}))
			if code != tc.code || (err != nil) != (tc.code != 0) {
				t.Fatalf("code=%d err=%v", code, err)
			}
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	code, _, err := ExecScriptContent(ctx, "sh", "#!/bin/sh\nsleep 10\n", WithScriptLogger(&captureTestLogger{}))
	if code != 137 || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation: code=%d err=%v", code, err)
	}
}

func TestLinuxScriptAndExecCancelBeforeStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "code")
	s := &script{cmd: exec.Command("/bin/sh"), exitCodePath: path, logger: &captureTestLogger{}}
	s.beforeExec()
	if err := s.cmd.Cancel(); err == nil {
		t.Fatal("cancel before start accepted")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "137" {
		t.Fatalf("cancel exit file: %q %v", data, err)
	}
	if s.cmd.SysProcAttr == nil || !s.cmd.SysProcAttr.Setpgid {
		t.Fatal("script does not create a process group")
	}
	ctx := t.Context()
	e := &XCmd{ctx: ctx, command: "/bin/sh"}
	e.initCmd()
	if err := e.Cancel(); err == nil {
		t.Fatal("command cancel before start accepted")
	}
	if s.transform("plain") != "plain" || s.utf8ToGb2312("plain") != "plain" {
		t.Fatal("Unix encoding unexpectedly transformed")
	}
}
