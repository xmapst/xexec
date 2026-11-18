package xexec

import (
	"fmt"
	"log/slog"
)

// ILogger is the script executor's logging interface, supporting custom backends.
//
// The interface decouples logging from script execution.
// Callers can inject implementations such as xlog or zap through WithScriptLogger.
// By default, the standard library's slog writes to stderr.
type ILogger interface {
	Debugf(format string, args ...any)
	Infof(format string, args ...any)
	Warnf(format string, args ...any)
	Errorf(format string, args ...any)
}

// defaultLogger implements logging through the standard library's slog.
//
// When args is empty, use format directly as the message,
// avoiding unnecessary fmt.Sprintf overhead.
type defaultLogger struct{}

func (l *defaultLogger) Debugf(format string, args ...any) {
	if len(args) == 0 {
		slog.Debug(format)
	} else {
		slog.Debug(fmt.Sprintf(format, args...))
	}
}

func (l *defaultLogger) Infof(format string, args ...any) {
	if len(args) == 0 {
		slog.Info(format)
	} else {
		slog.Info(fmt.Sprintf(format, args...))
	}
}

func (l *defaultLogger) Warnf(format string, args ...any) {
	if len(args) == 0 {
		slog.Warn(format)
	} else {
		slog.Warn(fmt.Sprintf(format, args...))
	}
}

func (l *defaultLogger) Errorf(format string, args ...any) {
	if len(args) == 0 {
		slog.Error(format)
	} else {
		slog.Error(fmt.Sprintf(format, args...))
	}
}
