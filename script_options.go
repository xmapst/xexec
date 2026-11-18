package xexec

import (
	"io"
)

// ScriptOption configures a script instance using the functional options pattern.
//
// Optional parameters stay explicit and extensible, without burdening callers with unused settings.
type ScriptOption func(*script)

// WithScriptWorkdir sets the script's working directory.
//
// This affects all relative paths in the script; the parent's working directory is inherited by default.
func WithScriptWorkdir(path string) ScriptOption {
	return func(s *script) {
		s.dir = path
	}
}

// WithScriptEnv sets additional environment variables for the script.
//
// Values are KEY=VALUE strings, merged with the system environment.
// Custom values override system variables with the same key.
func WithScriptEnv(env ...string) ScriptOption {
	return func(s *script) {
		s.env = env
	}
}

// WithScriptArgs sets the command-line arguments passed to the script.
//
// Arguments follow the script path in the interpreter invocation
// and are available through $1, $2, etc. or sys.argv inside the script.
func WithScriptArgs(args ...string) ScriptOption {
	return func(s *script) {
		s.args = args
	}
}

// WithScriptStdin sets the source of the script's standard input.
//
// Use it for interactive or piped data, such as passwords or configuration content.
func WithScriptStdin(stdin io.Reader) ScriptOption {
	return func(s *script) {
		s.stdin = stdin
	}
}

// WithScriptLogger replaces the default slog logger with a custom logger.
//
// Typically this is the application's logging implementation (such as xlog),
// so script and application logs share centralized collection and analysis.
func WithScriptLogger(logger ILogger) ScriptOption {
	return func(s *script) {
		s.logger = logger
	}
}

// WithScriptSecrets sets sensitive strings to redact from log output.
//
// All nonempty secrets are replaced with "***" before logging,
// preventing keys, passwords, and other sensitive values from leaking into log files.
// Empty strings are filtered out to avoid inserting "***" throughout the output.
func WithScriptSecrets(secrets ...string) ScriptOption {
	return func(s *script) {
		// Filter empty strings, which would otherwise match every position.
		var validSecrets []string
		for _, sec := range secrets {
			if len(sec) > 0 {
				validSecrets = append(validSecrets, sec)
			}
		}
		s.secrets = append(s.secrets, validSecrets...)
	}
}
