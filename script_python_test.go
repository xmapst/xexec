package xexec

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func requirePython(t *testing.T) {
	t.Helper()
	for _, name := range []string{"python3", "python", "py"} {
		if _, err := exec.LookPath(name); err == nil {
			return
		}
	}
	t.Skip("Python interpreter is not installed")
}

func TestPythonScriptExitCodes(t *testing.T) {
	requirePython(t)
	for _, tc := range []struct {
		name, content string
		code          int
		wantErr       bool
	}{
		{"normal", "print('ok')", 0, false},
		{"none", "import sys; sys.exit(None)", 0, false},
		{"integer", "import sys; sys.exit(7)", 7, true},
		{"message", "import sys; sys.exit('failure message')", 1, true},
		{"exception", "raise RuntimeError('expected failure')", 255, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			code, _, err := ExecScriptContent(ctx, "py", tc.content, WithScriptLogger(&captureTestLogger{}))
			if code != tc.code || (err != nil) != tc.wantErr {
				t.Fatalf("code=%d err=%v, want code=%d error=%t", code, err, tc.code, tc.wantErr)
			}
			if tc.wantErr {
				if _, ok := errors.AsType[*exec.ExitError](err); !ok {
					t.Fatalf("missing process exit error: %v", err)
				}
			}
		})
	}
}

func TestPythonScriptOptionsAndExportedEnvironment(t *testing.T) {
	requirePython(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dir := t.TempDir()
	content := `import os, sys
assert __name__ == '__main__'
assert sys.argv[1:] == ['hello world', '']
assert sys.stdin.read() == 'input data'
assert os.environ['XEXEC_PYTHON_TEST'] == 'custom'
with open(os.environ['XEXEC_SAVE_ENV_FILE'], 'w') as f:
    f.write('XEXEC_PYTHON_RESULT=ok\n')
    f.write('XEXEC_PYTHON_WORKDIR=' + os.getcwd() + '\n')
print('secret-value')
`
	code, env, err := ExecScriptContent(ctx, "py", content,
		WithScriptWorkdir(dir), WithScriptArgs("hello world", ""), WithScriptStdin(strings.NewReader("input data")),
		WithScriptEnv("XEXEC_PYTHON_TEST=custom"), WithScriptSecrets("", "secret-value"), WithScriptLogger(&captureTestLogger{}))
	if err != nil || code != 0 {
		t.Fatalf("python options: code=%d err=%v", code, err)
	}
	if env["XEXEC_PYTHON_RESULT"] != "ok" {
		t.Fatalf("exported environment: %#v", env)
	}
	workdir, ok := env["XEXEC_PYTHON_WORKDIR"].(string)
	if !ok {
		t.Fatalf("workdir got %#v, want %q", env["XEXEC_PYTHON_WORKDIR"], dir)
	}
	assertTestWorkingDirectory(t, workdir, dir)
}
