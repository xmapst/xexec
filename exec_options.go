package xexec

// CommandOption configures a command before it starts.
type CommandOption func(*XCmd)

// WithExecWorkingDir sets the working directory; empty inherits the parent directory.
func WithExecWorkingDir(workingDir string) CommandOption {
	return func(c *XCmd) {
		c.workingDir = workingDir
	}
}

// WithExecArgs sets the command arguments.
func WithExecArgs(args ...string) CommandOption {
	return func(c *XCmd) {
		c.args = args
	}
}

// WithExecEnv adds environment variables in "KEY=VALUE" format.
//
// Variables are appended to the inherited environment rather than replacing it.
func WithExecEnv(envs ...string) CommandOption {
	return func(c *XCmd) {
		c.extraEnv = envs
	}
}
