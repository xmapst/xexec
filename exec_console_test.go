package xexec

import (
	"bytes"
	"os"
	"sync"
	"testing"
)

func TestConsoleFilesAreIndependent(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	a, err := newConsoleFile("same-command", []string{"same-argument"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	defer os.Remove(a.Name())
	b, err := newConsoleFile("same-command", []string{"same-argument"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	defer os.Remove(b.Name())
	if a.Name() == b.Name() {
		t.Fatal("separate executions share a console file")
	}
	for _, write := range []struct {
		file *consoleFile
		data string
	}{{a, "AAAAAA"}, {b, "BBBBBB"}, {a, "AAAAAA"}, {b, "BB"}, {a, "CC"}} {
		if _, err := write.file.Write([]byte(write.data)); err != nil {
			t.Fatal(err)
		}
	}
	for file, want := range map[*consoleFile]string{a: "AAAAAACC", b: "BBBBBBBB"} {
		data, err := os.ReadFile(file.Name())
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != want || len(data) > 10 {
			t.Fatalf("console got %q, want %q within limit", data, want)
		}
	}
}

func TestConsoleFileConcurrentWritesAndOversizedTail(t *testing.T) {
	file, err := newConsoleFile("concurrent-command", nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(file.Name())
	defer file.Close()
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 20 {
				if _, err := file.Write([]byte("abc")); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	data := []byte("0123456789ABCDEFGHIJ")
	if n, err := file.Write(data); err != nil || n != len(data) {
		t.Fatalf("oversized write: n=%d err=%v", n, err)
	}
	got, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte("ABCDEFGHIJ")) {
		t.Fatalf("tail got %q", got)
	}
}
