package xexec

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestExecContextInvalidInput(t *testing.T) {
	for _, command := range []string{"", " \t\n"} {
		cmd, err := ExecContext(nil, command)
		if cmd != nil || !errors.Is(err, errEmptyCommand) {
			t.Fatalf("empty command: %v %v", cmd, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if cmd, err := ExecContext(ctx, os.Args[0], nil); cmd != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled start: %v %v", cmd, err)
	}
	if cmd, err := ExecContext(nil, filepath.Join(t.TempDir(), "no-such-executable"), nil); cmd != nil || err == nil {
		t.Fatalf("missing command: %v %v", cmd, err)
	}
}

func TestExecOptionsAndUninitializedCommand(t *testing.T) {
	e := &XCmd{}
	WithExecWorkingDir("workdir")(e)
	WithExecEnv("A=B")(e)
	WithExecArgs("a", "b")(e)
	if e.workingDir != "workdir" || !reflect.DeepEqual(e.extraEnv, []string{"A=B"}) || !reflect.DeepEqual(e.args, []string{"a", "b"}) {
		t.Fatalf("incorrect options: %+v", e)
	}
	if !errors.Is(e.Wait(), errCommandNotInitialized) || !errors.Is(e.attachConsoleOutput(), errCommandNotInitialized) {
		t.Fatal("uninitialized command accepted")
	}
	e.closeConsoleOutput()
}

func TestExecStartOutputFailure(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	t.Setenv("TMPDIR", dir)
	t.Setenv("TMP", dir)
	t.Setenv("TEMP", dir)
	if cmd, err := ExecContext(nil, os.Args[0]); cmd != nil || err == nil {
		t.Fatalf("invalid output directory: %v %v", cmd, err)
	}
}

type failingProcessOutput struct{ syncCalls, closeCalls int }

func (*failingProcessOutput) Write(p []byte) (int, error) { return len(p), nil }
func (*failingProcessOutput) Name() string                { return "test-output" }
func (o *failingProcessOutput) Sync() error               { o.syncCalls++; return errors.New("sync failed") }
func (o *failingProcessOutput) Close() error              { o.closeCalls++; return errors.New("close failed") }
func TestExecOutputCloseErrorsAndIdempotency(t *testing.T) {
	output := &failingProcessOutput{}
	e := &XCmd{output: output}
	e.closeConsoleOutput()
	e.closeConsoleOutput()
	if e.output != nil || output.syncCalls != 1 || output.closeCalls != 1 {
		t.Fatalf("close did not release output: %+v", output)
	}
}

func TestConsoleFileNilClosedAndInvalidStates(t *testing.T) {
	var nilFile *consoleFile
	if nilFile.Name() != "" || nilFile.Sync() != nil || nilFile.Close() != nil {
		t.Fatal("nil console methods failed")
	}
	if _, err := nilFile.Write(nil); !errors.Is(err, os.ErrInvalid) {
		t.Fatalf("nil write: %v", err)
	}
	file := &consoleFile{}
	if _, err := file.Write([]byte("x")); !errors.Is(err, os.ErrInvalid) {
		t.Fatalf("uninitialized write: %v", err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(nil); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closed write: %v", err)
	}
}

func TestConsoleFileDefaultsAndErrors(t *testing.T) {
	file, err := newConsoleFile("/strange/name !?.exe", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if file.maxSize != defaultConsoleMaxSize {
		t.Fatal("invalid size did not use default")
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	file.file.Close() // Simulate a failed underlying file handle.
	if _, err := file.Write([]byte("x")); err == nil {
		t.Fatal("failed write accepted")
	}
	file.maxSize = 1
	if _, err := file.Write([]byte("oversized")); err == nil {
		t.Fatal("failed tail reset accepted")
	}
	file.currentSize = 1
	if _, err := file.Write([]byte("x")); err == nil {
		t.Fatal("failed normal reset accepted")
	}
	if err := file.Sync(); err == nil {
		t.Fatal("failed sync accepted")
	}
	if err := file.Close(); err == nil {
		t.Fatal("failed close accepted")
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	if consoleLogFilename("", nil) == "" || sanitizeFilenamePart("Az09-_. !") != "Az09-_.--" {
		t.Fatal("filename sanitization failed")
	}
	if commandFingerprint("program", []string{"a", "b"}) == commandFingerprint("program", []string{"a b"}) {
		t.Fatal("fingerprint loses argument boundaries")
	}
}

func TestReaperOwnershipAndOpaqueWait(t *testing.T) {
	r := &childReaper{managed: make(map[int]*exec.Cmd), changed: make(chan struct{}, 1)}
	owned := &exec.Cmd{}
	r.managed[123] = owned
	called := false
	wantErr := errors.New("scan failed")
	if err := r.withUnowned(func(isOwned func(int) bool) error {
		called = true
		if !isOwned(123) || isOwned(456) {
			t.Error("wrong ownership registry")
		}
		return wantErr
	}); !errors.Is(err, wantErr) || !called {
		t.Fatalf("scan result: called=%t err=%v", called, err)
	}
	release1 := r.beginOpaqueWait()
	release2 := r.beginOpaqueWait()
	called = false
	if err := r.withUnowned(func(func(int) bool) error { called = true; return nil }); err != nil || called {
		t.Fatal("scan ran during opaque wait")
	}
	release1()
	release1()
	if r.opaque != 1 {
		t.Fatalf("nested count=%d", r.opaque)
	}
	release2()
	release2()
	if r.opaque != 0 {
		t.Fatalf("released count=%d", r.opaque)
	}
	r.notifyChange()
	r.notifyChange()
	if len(r.changed) != 1 {
		t.Fatal("change notifications are not coalesced")
	}
}
