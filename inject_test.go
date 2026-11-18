package xexec

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestInjectPlaceholderStylesAndPrecedence(t *testing.T) {
	t.Setenv("XEXEC_TEST_INJECT", "system")
	for _, placeholder := range []string{"%XEXEC_TEST_INJECT%", "${XEXEC_TEST_INJECT}", "$XEXEC_TEST_INJECT", "{{ XEXEC_TEST_INJECT }}", "{ XEXEC_TEST_INJECT }"} {
		if got := Inject(placeholder, map[string]any{"XEXEC_TEST_INJECT": 42}); got != "42" {
			t.Errorf("%q: got %q", placeholder, got)
		}
		if got := Inject(placeholder, nil); got != "system" {
			t.Errorf("environment fallback %q: got %q", placeholder, got)
		}
	}
	unknown := "XEXEC_TEST_UNDEFINED_9E719"
	t.Setenv(unknown, "")
	// Use another name that is deliberately absent, not an empty environment value.
	unknown += "_ABSENT"
	for input, want := range map[string]string{
		"%" + unknown + "%":     "%" + unknown + "%",
		"${" + unknown + "}":    "${" + unknown + "}",
		"$" + unknown:           "$" + unknown,
		"{{ " + unknown + " }}": "{{" + unknown + "}}",
		"{ " + unknown + " }":   "{" + unknown + "}",
		"ordinary text":         "ordinary text",
		"":                      "",
	} {
		if got := Inject(input, nil); got != want {
			t.Errorf("unknown %q: got %q, want %q", input, got, want)
		}
	}
	if got := Inject("${VALUE}", map[string]any{"VALUE": "$OTHER & %PATH%"}); got != "$OTHER & %PATH%" {
		t.Fatalf("replacement recursively expanded: %q", got)
	}
}

func TestInjectArgs(t *testing.T) {
	args := []string{"--name", "${NAME}", "{{ COUNT }}", ""}
	got := InjectArgs(args, map[string]any{"NAME": "hello world", "COUNT": 3})
	want := []string{"--name", "hello world", "3", ""}
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(args, want) {
		t.Fatalf("in-place replacement got %#v, args %#v", got, args)
	}
	if InjectArgs(nil, nil) != nil {
		t.Fatal("nil arguments must remain nil")
	}
}

func TestInjectFile(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
		wantErr           bool
	}{
		{"replace", "name=${NAME}\ncount={{ COUNT }}\n", "name=hello world\ncount=3\n", false},
		{"unchanged", "unchanged without trailing newline", "unchanged without trailing newline", false},
		{"empty", "", "", false},
		{"scanner-error", strings.Repeat("x", 70*1024), strings.Repeat("x", 70*1024), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.txt")
			if err := os.WriteFile(path, []byte(tc.input), 0600); err != nil {
				t.Fatal(err)
			}
			err := InjectFile(path, map[string]any{"NAME": "hello world", "COUNT": 3})
			if (err != nil) != tc.wantErr {
				t.Fatalf("error got %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("file got %q, want %q", got, tc.want)
			}
			if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
				t.Fatalf("temporary file remains: %v", err)
			}
		})
	}
	if err := InjectFile(filepath.Join(t.TempDir(), "missing"), nil); err != nil {
		t.Fatal(err)
	}
	// A directory occupying the temporary path must fail without overwriting input.
	dir := t.TempDir()
	path := filepath.Join(dir, "blocked")
	if err := os.WriteFile(path, []byte("${NAME}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path+".tmp", 0700); err != nil {
		t.Fatal(err)
	}
	if err := InjectFile(path, map[string]any{"NAME": "replacement"}); err == nil {
		t.Fatal("blocked temporary file accepted")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "${NAME}" {
		t.Fatalf("original modified: %q %v", got, err)
	}
}
