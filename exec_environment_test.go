package xexec

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestExecWorkingDirAndEnvironment(t *testing.T) {
	const helper = "XEXEC_WORKDIR_HELPER"
	if os.Getenv(helper) == "1" {
		dir, err := os.Getwd()
		if err != nil {
			os.Exit(2)
		}
		if err := json.NewEncoder(os.Stdout).Encode(map[string]string{
			"dir":       dir,
			"override":  os.Getenv("XEXEC_WORKDIR_OVERRIDE"),
			"inherited": os.Getenv("XEXEC_WORKDIR_INHERITED"),
		}); err != nil {
			os.Exit(3)
		}
		os.Exit(0)
	}
	t.Setenv("XEXEC_WORKDIR_OVERRIDE", "system")
	t.Setenv("XEXEC_WORKDIR_INHERITED", "inherited")
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd, err := ExecContext(ctx, os.Args[0],
		WithExecWorkingDir(dir),
		WithExecArgs("-test.run=^TestExecWorkingDirAndEnvironment$"),
		WithExecEnv(helper+"=1", "XEXEC_WORKDIR_OVERRIDE=custom"),
	)
	if err != nil {
		t.Fatal(err)
	}
	path := cmd.output.Name()
	defer os.Remove(path)
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode output %q: %v", data, err)
	}
	if got["override"] != "custom" || got["inherited"] != "inherited" {
		t.Fatalf("working directory/environment got %#v", got)
	}
	assertTestWorkingDirectory(t, got["dir"], dir)
}
