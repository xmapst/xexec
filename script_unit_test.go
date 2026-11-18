package xexec

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type captureTestLogger struct {
	mu       sync.Mutex
	messages []string
}

func (l *captureTestLogger) record(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.messages = append(l.messages, fmt.Sprintf(format, args...))
}
func (l *captureTestLogger) Debugf(f string, a ...any) { l.record(f, a...) }
func (l *captureTestLogger) Infof(f string, a ...any)  { l.record(f, a...) }
func (l *captureTestLogger) Warnf(f string, a ...any)  { l.record(f, a...) }
func (l *captureTestLogger) Errorf(f string, a ...any) { l.record(f, a...) }
func (l *captureTestLogger) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.messages, "\n")
}

func TestScriptOptions(t *testing.T) {
	logger := &captureTestLogger{}
	input := strings.NewReader("input")
	s := &script{}
	for _, option := range []ScriptOption{
		WithScriptWorkdir("workdir"), WithScriptEnv("KEY=value"), WithScriptArgs("a", "hello world"),
		WithScriptStdin(input), WithScriptLogger(logger), WithScriptSecrets("", "secret", ""),
	} {
		option(s)
	}
	if s.dir != "workdir" || s.stdin != input || s.logger != logger ||
		!reflect.DeepEqual(s.env, []string{"KEY=value"}) || !reflect.DeepEqual(s.args, []string{"a", "hello world"}) ||
		!reflect.DeepEqual(s.secrets, []string{"secret"}) {
		t.Fatalf("incorrect options: %+v", s)
	}
}

func TestScriptMergeEnvAndKeys(t *testing.T) {
	t.Setenv("XEXEC_UNIT_OVERRIDE", "system")
	s := &script{env: []string{"XEXEC_UNIT_OVERRIDE=custom", "NEW_KEY=with=equals", "invalid", "=empty", "XEXEC_TARGET=forged"}, path: "real-target", exitCodePath: "exit-file", saveEnvPath: "env-file"}
	values := make(map[string]string)
	for _, entry := range s.mergeEnv() {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = value
	}
	for key, want := range map[string]string{"XEXEC_UNIT_OVERRIDE": "custom", "NEW_KEY": "with=equals", "XEXEC_TARGET": "real-target", "XEXEC_EXIT_CODE_FILE": "exit-file", "XEXEC_SAVE_ENV_FILE": "env-file"} {
		if values[key] != want {
			t.Errorf("%s got %q", key, values[key])
		}
	}
	if _, ok := values[""]; ok {
		t.Fatal("empty environment key retained")
	}
	for input, want := range map[string]string{"KEY=value": "KEY", "A=b=c": "A", "invalid": "", "=value": ""} {
		if got := s.getEnvKey(input); got != want {
			t.Errorf("key %q got %q", input, got)
		}
	}
}

func TestScriptParseExitCode(t *testing.T) {
	for _, tc := range []struct {
		text  string
		code  int
		valid bool
	}{
		{"0", 0, true}, {"137\r\n", 137, true}, {"exit: 255", 255, true}, {"", 255, false}, {"not a number", 255, false}, {strings.Repeat("9", 100), 255, false},
	} {
		t.Run(fmt.Sprintf("%d-%t-%d", tc.code, tc.valid, len(tc.text)), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "code")
			if err := os.WriteFile(path, []byte(tc.text), 0600); err != nil {
				t.Fatal(err)
			}
			code, err := (&script{exitCodePath: path}).parseExitCodeFromFile()
			if (err == nil) != tc.valid || (tc.valid && code != tc.code) {
				t.Fatalf("parse %q: code=%d err=%v", tc.text, code, err)
			}
		})
	}
	if _, err := (&script{exitCodePath: filepath.Join(t.TempDir(), "missing")}).parseExitCodeFromFile(); err == nil {
		t.Fatal("missing code accepted")
	}
}

func TestScriptParseSaveEnv(t *testing.T) {
	t.Setenv("XEXEC_SAVE_PROTECTED", "system")
	for _, tc := range []struct {
		name, content string
		want          map[string]any
	}{
		{"single-and-multiline", "\xef\xbb\xbfSINGLE=with=equals\n\nMULTI<<END\nfirst\nsecond\nEND\nVALUE=value<<ordinary\nXEXEC_SAVE_PROTECTED=forged\n", map[string]any{"SINGLE": "with=equals", "MULTI": "first\nsecond", "VALUE": "value<<ordinary"}},
		{"invalid-lines", "=empty-key\n<<END\nKEY<<\nnot-valid\nGOOD=ok\n", map[string]any{"GOOD": "ok"}},
		{"missing-delimiter", "GOOD=ok\nBROKEN<<END\nunfinished\n", map[string]any{"GOOD": "ok"}},
		{"protected-multiline", "XEXEC_SAVE_PROTECTED<<END\nforged\nEND\nGOOD=ok\n", map[string]any{"GOOD": "ok"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "env")
			if err := os.WriteFile(path, []byte(tc.content), 0600); err != nil {
				t.Fatal(err)
			}
			s := &script{saveEnvPath: path, logger: &captureTestLogger{}}
			if got := s.parseSaveEnvFile(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("env got %#v, want %#v", got, tc.want)
			}
		})
	}
	if got := (&script{saveEnvPath: filepath.Join(t.TempDir(), "missing"), logger: &captureTestLogger{}}).parseSaveEnvFile(); len(got) != 0 {
		t.Fatalf("missing env got %#v", got)
	}
}

func TestScriptInvalidPathsAndTempDirectory(t *testing.T) {
	if code, _, err := ExecScript(nil, filepath.Join(t.TempDir(), "missing")); code != 255 || err == nil {
		t.Fatalf("missing script: %d %v", code, err)
	}
	dir := filepath.Join(t.TempDir(), "missing-temp-dir")
	// os.TempDir consults TMPDIR on Unix and TMP on Windows.
	t.Setenv("TMPDIR", dir)
	t.Setenv("TMP", dir)
	t.Setenv("TEMP", dir)
	if code, _, err := ExecScriptContent(context.Background(), "sh", "exit 0"); code != 255 || err == nil {
		t.Fatalf("invalid temp dir: %d %v", code, err)
	}
}

var _ ILogger = (*captureTestLogger)(nil)
var _ io.Reader = (*strings.Reader)(nil)
