package xexec_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmapst/xexec"
)

func TestPackageErrorsAsType(t *testing.T) {
	_, emptyErr := xexec.ExecContext(context.Background(), " ")
	var cmd xexec.XCmd
	for name, err := range map[string]error{
		"empty command":         emptyErr,
		"uninitialized command": cmd.Wait(),
		"invalid PID":           xexec.KillAll(0),
	} {
		t.Run(name, func(t *testing.T) {
			got, ok := errors.AsType[*xexec.Error](err)
			if !ok || got.Op == "" || got.Err != nil {
				t.Fatalf("expected contextual package error without cause, got %#v", err)
			}
		})
	}
}

func TestInvalidPIDErrorPreservesSignedValue(t *testing.T) {
	for _, pid := range []int32{-1 << 31, -1, 0} {
		err := xexec.KillAll(pid)
		got, ok := errors.AsType[*xexec.Error](err)
		if !ok || got.Op != "kill all: invalid pid" || got.PID != int64(pid) || got.Err != nil {
			t.Fatalf("PID %d: unexpected error %#v", pid, err)
		}
	}
}

func TestErrorFormatsContextAndUnwraps(t *testing.T) {
	cause := errors.New("underlying failure")
	cases := []struct {
		name string
		err  *xexec.Error
		want string
	}{
		{"operation only", &xexec.Error{Op: "operation"}, "operation"},
		{"negative PID", &xexec.Error{Op: "operation", PID: -1}, "operation pid=-1"},
		{"full Windows PID", &xexec.Error{Op: "operation", PID: 1<<32 - 1}, "operation pid=4294967295"},
		{"path", &xexec.Error{Op: "operation", Path: "a b"}, `operation "a b"`},
		{"value", &xexec.Error{Op: "operation", Value: "a\nb"}, `operation "a\nb"`},
		{"cause", &xexec.Error{Op: "operation", Err: cause}, "operation: underlying failure"},
		{"all context", &xexec.Error{Op: "operation", PID: 42, Path: "path", Value: "value", Err: cause}, `operation pid=42 "path" "value": underlying failure`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Fatalf("Error() = %q, want %q", got, tc.want)
			}
			if got := errors.Unwrap(tc.err); got != tc.err.Err {
				t.Fatalf("Unwrap() = %v, want %v", got, tc.err.Err)
			}
		})
	}
}

func TestScriptErrorPreservesPathAndCause(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-script.sh")
	_, _, err := xexec.ExecScript(context.Background(), path)
	got, ok := errors.AsType[*xexec.Error](err)
	if !ok || got.Op != "stat script" || got.Path != path {
		t.Fatalf("unexpected script error: %#v", err)
	}
	underlying, ok := errors.AsType[*os.PathError](err)
	if !ok || underlying.Path != path || !errors.Is(err, os.ErrNotExist) || errors.Unwrap(got) != underlying {
		t.Fatalf("lost original path error: %v", err)
	}
}

func TestInjectionErrorPreservesOriginalCause(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input.txt")
	if err := os.WriteFile(path, []byte("$X"), 0600); err != nil {
		t.Fatal(err)
	}
	// A directory at the temporary-file path forces a portable open failure,
	// including when tests run as root.
	if err := os.Mkdir(path+".tmp", 0700); err != nil {
		t.Fatal(err)
	}
	err := xexec.InjectFile(path, map[string]any{"X": "replacement"})
	got, ok := errors.AsType[*xexec.Error](err)
	if !ok || got.Op != "create injection temporary file" || got.Path != path+".tmp" {
		t.Fatalf("unexpected injection error: %#v", err)
	}
	underlying, ok := errors.AsType[*os.PathError](err)
	if !ok || !errors.Is(err, underlying.Err) || errors.Unwrap(got) != underlying {
		t.Fatalf("lost original open error: %v", err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil || string(data) != "$X" {
		t.Fatalf("original file changed: %q, %v", data, readErr)
	}
}

func TestErrorUnwrapJoinedCauses(t *testing.T) {
	first := &os.PathError{Op: "open", Path: "input", Err: os.ErrNotExist}
	err := &xexec.Error{Op: "cancel process", PID: 42, Path: "script", Value: "context", Err: errors.Join(first, context.Canceled)}
	if !errors.Is(err, os.ErrNotExist) || !errors.Is(err, context.Canceled) {
		t.Fatalf("joined causes lost: %v", err)
	}
	if got, ok := errors.AsType[*os.PathError](err); !ok || got != first {
		t.Fatalf("underlying type lost: %v", err)
	}
	for _, part := range []string{"cancel process", "42", "script", "context", "input"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("error text %q missing %q", err, part)
		}
	}
}

func TestNativeExecErrorsRemainRecognizable(t *testing.T) {
	dir := t.TempDir()
	invalid := filepath.Join(dir, "invalid-command.exe")
	if err := os.WriteFile(invalid, []byte("not an executable\n"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, command := range map[string]string{
		"missing executable": filepath.Join(dir, "nonexistent-command"),
		"invalid executable": invalid,
	} {
		t.Run(name, func(t *testing.T) {
			// Missing programs can fail during Windows LookPath (*exec.Error)
			// or Unix process startup (*os.PathError). Compare with the native
			// API on this platform rather than assuming a single error type.
			want := exec.Command(command).Start()
			_, err := xexec.ExecContext(context.Background(), command)
			switch native := want.(type) {
			case *exec.Error:
				got, ok := errors.AsType[*exec.Error](err)
				if !ok || got.Name != native.Name || !errors.Is(err, native.Err) {
					t.Fatalf("native lookup error lost: got %T %v, want %v", err, err, want)
				}
			case *os.PathError:
				got, ok := errors.AsType[*os.PathError](err)
				if !ok || got.Op != native.Op || got.Path != native.Path || !errors.Is(err, native.Err) {
					t.Fatalf("native start error lost: got %T %v, want %v", err, err, want)
				}
			default:
				t.Fatalf("unexpected standard-library start result: %T %v", want, want)
			}
		})
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := xexec.ExecContext(context.Background(), executable,
		xexec.WithExecArgs("-test.run=^TestNativeExecErrorExitHelper$"),
		xexec.WithExecEnv("XEXEC_ERROR_EXIT_HELPER=1"))
	if err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	if exitErr, ok := errors.AsType[*exec.ExitError](err); !ok || exitErr.ExitCode() != 7 {
		t.Fatalf("native exit error lost: %T %v", err, err)
	}
}

func TestNativeExecErrorExitHelper(t *testing.T) {
	if os.Getenv("XEXEC_ERROR_EXIT_HELPER") == "1" {
		os.Exit(7)
	}
}
