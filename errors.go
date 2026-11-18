package xexec

import (
	"fmt"
	"strings"
)

// Error describes an error defined or wrapped by xexec. Native errors returned
// directly by os and os/exec retain their original types; callers should not
// assume every failure is an *Error. Use errors.AsType[*Error] to inspect package
// errors and errors.Is or errors.AsType to inspect their underlying causes.
//
// Op identifies the operation or validation failure. PID, Path, and Value hold
// relevant context when available; zero or empty values mean unspecified.
// PID is signed so invalid negative PIDs can be reported without losing their
// original value, while also representing the full Windows uint32 PID range.
// Err is the underlying cause, if any, and may itself contain joined errors.
type Error struct {
	Op    string
	PID   int64
	Path  string
	Value string
	Err   error
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(e.Op)
	if e.PID != 0 {
		_, _ = fmt.Fprintf(&b, " pid=%d", e.PID)
	}
	if e.Path != "" {
		_, _ = fmt.Fprintf(&b, " %q", e.Path)
	}
	if e.Value != "" {
		_, _ = fmt.Fprintf(&b, " %q", e.Value)
	}
	if e.Err != nil {
		_, _ = fmt.Fprintf(&b, ": %v", e.Err)
	}
	return b.String()
}

// Unwrap exposes the original cause without losing errors.Is/AsType support.
func (e *Error) Unwrap() error { return e.Err }

var (
	errEmptyCommand          = &Error{Op: "xexec: command is empty"}
	errCommandNotInitialized = &Error{Op: "xexec: command is not initialized"}
	errProcessNotStarted     = &Error{Op: "process not started"}
)
