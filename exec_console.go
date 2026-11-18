package xexec

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// processOutput is the minimal interface for a command output destination.
type processOutput interface {
	io.Writer
	Name() string
	Sync() error
	Close() error
}

// consoleFile is a size-limited console log.
//
// Writes reset the file when needed to keep console output within maxSize.
// Its methods are safe for concurrent stdout and stderr copy goroutines.
type consoleFile struct {
	file        *os.File
	filename    string
	maxSize     int64
	currentSize int64
	sync.Mutex
	closed bool
}

func newConsoleFile(command string, args []string, maxSize int64) (*consoleFile, error) {
	if maxSize <= 0 {
		maxSize = defaultConsoleMaxSize
	}

	// Each execution owns a private file, lock, size counter, and rotation state.
	// CreateTemp also prevents predictable paths from following preexisting symlinks.
	pattern := strings.TrimSuffix(consoleLogFilename(command, args), ".log") + "-*.log"
	file, err := os.CreateTemp(os.TempDir(), pattern)
	if err != nil {
		return nil, &Error{Op: "create console log", Path: filepath.Join(os.TempDir(), pattern), Err: err}
	}
	return &consoleFile{
		file:     file,
		filename: file.Name(),
		maxSize:  maxSize,
	}, nil
}

func consoleLogFilename(command string, args []string) string {
	base := filepath.Base(command)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	base = sanitizeFilenamePart(base)
	if base == "" {
		base = "process"
	}
	return fmt.Sprintf("console-%s-%s.log", base, commandFingerprint(command, args))
}

func sanitizeFilenamePart(value string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '-', r == '_', r == '.':
			return r
		default:
			return '-'
		}
	}, value)
}

func commandFingerprint(command string, args []string) string {
	hash := md5.New() //nolint:gosec // Used only for local log filenames, not for security.
	_, _ = io.WriteString(hash, command)
	for _, arg := range args {
		_, _ = io.WriteString(hash, "\x00")
		_, _ = io.WriteString(hash, arg)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// Name returns the full log path.
func (c *consoleFile) Name() string {
	if c == nil {
		return ""
	}
	return c.filename
}

// Write appends output, resetting old content when needed.
func (c *consoleFile) Write(p []byte) (int, error) {
	if c == nil {
		return 0, os.ErrInvalid
	}

	c.Lock()
	defer c.Unlock()

	if c.closed {
		return 0, os.ErrClosed
	}
	if c.file == nil {
		return 0, os.ErrInvalid
	}

	if c.maxSize > 0 && int64(len(p)) > c.maxSize {
		return c.writeTailLocked(p)
	}
	if c.shouldResetBeforeWrite(len(p)) {
		if err := c.resetLocked(); err != nil {
			return 0, err
		}
	}

	n, err := c.file.Write(p)
	c.currentSize += int64(n)
	return n, err
}

// Sync flushes buffered data to disk.
func (c *consoleFile) Sync() error {
	if c == nil {
		return nil
	}

	c.Lock()
	defer c.Unlock()

	if c.closed || c.file == nil {
		return nil
	}
	return c.file.Sync()
}

// Close closes the file handle and is safe to repeat.
func (c *consoleFile) Close() error {
	if c == nil {
		return nil
	}

	c.Lock()
	defer c.Unlock()

	if c.closed {
		return nil
	}
	c.closed = true
	if c.file == nil {
		return nil
	}

	err := c.file.Close()
	c.file = nil
	c.currentSize = 0
	return err
}

func (c *consoleFile) writeTailLocked(p []byte) (int, error) {
	if err := c.resetLocked(); err != nil {
		return 0, err
	}

	tailOffset := len(p) - int(c.maxSize)
	tail := p[tailOffset:]
	n, err := c.file.Write(tail)
	c.currentSize += int64(n)
	if err != nil {
		return tailOffset + n, err
	}
	if n != len(tail) {
		return tailOffset + n, io.ErrShortWrite
	}
	return len(p), nil
}

func (c *consoleFile) shouldResetBeforeWrite(writeLen int) bool {
	return c.maxSize > 0 && c.currentSize+int64(writeLen) > c.maxSize
}

func (c *consoleFile) resetLocked() error {
	if err := c.file.Truncate(0); err != nil {
		return &Error{Op: "reset console log", Path: c.filename, Err: err}
	}
	c.currentSize = 0
	if _, err := c.file.Seek(0, io.SeekStart); err != nil {
		return &Error{Op: "seek console log", Path: c.filename, Err: err}
	}
	return nil
}
