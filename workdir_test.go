package xexec

import (
	"os"
	"testing"
)

// Compare directory identity instead of spelling: Windows processes may return
// either an 8.3 short path or its long form, and other platforms may resolve links.
func assertTestWorkingDirectory(t *testing.T, got, want string) {
	t.Helper()
	gotInfo, err := os.Stat(got)
	if err != nil {
		t.Fatalf("stat reported working directory %q: %v", got, err)
	}
	wantInfo, err := os.Stat(want)
	if err != nil {
		t.Fatalf("stat expected working directory %q: %v", want, err)
	}
	if !gotInfo.IsDir() || !wantInfo.IsDir() || !os.SameFile(gotInfo, wantInfo) {
		t.Fatalf("working directory %q does not identify expected directory %q", got, want)
	}
}
