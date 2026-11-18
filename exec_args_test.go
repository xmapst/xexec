package xexec

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"
)

func TestExecPreservesArguments(t *testing.T) {
	const helperEnv = "XEXEC_ARGUMENT_HELPER"
	if os.Getenv(helperEnv) == "1" {
		for i, arg := range os.Args {
			if arg == "--" {
				if err := json.NewEncoder(os.Stdout).Encode(os.Args[i+1:]); err != nil {
					os.Exit(2)
				}
				os.Exit(0)
			}
		}
		os.Exit(3)
	}
	// Exercise a real executable path with spaces, including on Windows.
	dir := filepath.Join(t.TempDir(), "directory with spaces")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	name := "argument helper"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(dir, name)
	src, err := os.Open(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(dst, src)
	closeErr := dst.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatalf("copy helper: %v, close: %v", copyErr, closeErr)
	}
	want := []string{"hello world", "", "&", "|", ">", "%PATH%", `a"b`, `C:\path with spaces\`, "中文"}
	args := append([]string{"-test.run=^TestExecPreservesArguments$", "--"}, want...)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd, err := ExecContext(ctx, path, WithExecArgs(args...), WithExecEnv(helperEnv+"=1"))
	if err != nil {
		t.Fatal(err)
	}
	logPath := cmd.output.Name()
	defer os.Remove(logPath)
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode %q: %v", data, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments got %#v, want %#v", got, want)
	}
}
