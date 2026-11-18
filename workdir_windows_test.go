//go:build windows

package xexec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsWorkingDirectoryShortAndLongPaths(t *testing.T) {
	longPath := filepath.Join(t.TempDir(), "directory with a long name")
	if err := os.Mkdir(longPath, 0700); err != nil {
		t.Fatal(err)
	}
	pathPtr, err := windows.UTF16PtrFromString(longPath)
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]uint16, 32768)
	n, err := windows.GetShortPathName(pathPtr, &buffer[0], uint32(len(buffer)))
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 || n >= uint32(len(buffer)) {
		t.Fatalf("unexpected short path length %d", n)
	}
	shortPath := windows.UTF16ToString(buffer[:n])
	if strings.EqualFold(shortPath, longPath) {
		t.Skip("8.3 short names are disabled on this volume")
	}
	assertTestWorkingDirectory(t, shortPath, longPath)
	assertTestWorkingDirectory(t, longPath, shortPath)
}
