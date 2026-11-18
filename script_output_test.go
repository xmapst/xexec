package xexec

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestConsoleOutputLinesAndRedaction(t *testing.T) {
	logger := &captureTestLogger{}
	s := &script{ctx: context.Background(), logger: logger, masker: strings.NewReplacer("secret", "***")}
	s.consoleOutput("STDOUT", io.NopCloser(strings.NewReader("first\r\nsecret\n\nlast fragment")))
	text := logger.text()
	for _, want := range []string{"[STDOUT] first", "[STDOUT] ***", "[STDOUT] \n", "[STDOUT] last fragment", "stop STDOUT"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in %q", want, text)
		}
	}
	if strings.Contains(text, "secret") || strings.Contains(text, "\r") {
		t.Fatalf("unredacted/CR output: %q", text)
	}
}

func TestConsoleOutputFlushesBeforeEOF(t *testing.T) {
	logger := &captureTestLogger{}
	reader, writer := io.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	defer reader.Close()
	defer writer.Close()
	done := make(chan struct{})
	go func() { (&script{ctx: ctx, logger: logger}).consoleOutput("STDOUT", reader); close(done) }()
	if _, err := writer.Write([]byte("progress")); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for !strings.Contains(logger.text(), "[STDOUT] progress") {
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("partial output was not flushed before EOF")
		}
	}
	writer.Close()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("console reader did not finish")
	}
}

func TestConsoleOutputCancellation(t *testing.T) {
	logger := &captureTestLogger{}
	reader, writer := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer reader.Close()
	defer writer.Close()
	done := make(chan struct{})
	go func() { (&script{ctx: ctx, logger: logger}).consoleOutput("STDERR", reader); close(done) }()
	if _, err := writer.Write([]byte("before cancel\n")); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("canceled output reader blocked")
	}
	if !strings.Contains(logger.text(), "stop STDERR") {
		t.Fatalf("missing shutdown log: %q", logger.text())
	}
}

func TestDefaultLoggerLevelsAndFormatting(t *testing.T) {
	// Do not run in parallel: temporarily replace the global slog default.
	var out strings.Builder
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(previous)
	logger := &defaultLogger{}
	for _, log := range []func(string, ...any){logger.Debugf, logger.Infof, logger.Warnf, logger.Errorf} {
		log("plain message")
		log("formatted %d", 42)
	}
	text := out.String()
	for _, level := range []string{"DEBUG", "INFO", "WARN", "ERROR"} {
		if !strings.Contains(text, "level="+level) {
			t.Errorf("missing level %s", level)
		}
	}
	if strings.Count(text, "plain message") != 4 || strings.Count(text, "formatted 42") != 4 {
		t.Fatalf("incorrect formatted logs: %q", text)
	}
}
