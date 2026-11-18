package xexec

import (
	"bytes"
	"os"
	"regexp"
	"strconv"
)

var (
	// digitRe matches consecutive digits to extract a number from the exit-code file.
	digitRe = regexp.MustCompile(`\d+`)
)

// parseExitCodeFromFile reads and parses the exit code from its file.
//
// The wrapper writes the script's exit code before exiting,
// preserving script-level exit values independently of ProcessState.ExitCode().
func (s *script) parseExitCodeFromFile() (int, error) {
	data, err := os.ReadFile(s.exitCodePath)
	if err != nil {
		return 255, &Error{Op: "read code file", Path: s.exitCodePath, Err: err}
	}

	matches := digitRe.FindString(string(bytes.TrimSpace(data)))
	if matches == "" {
		return 255, &Error{Op: "no valid exit code found", Path: s.exitCodePath, Value: string(data)}
	}

	return strconv.Atoi(matches)
}
