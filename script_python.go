package xexec

import (
	"os"
	"os/exec"
	"path/filepath"
)

// execPythonScript executes the target Python file through a Python wrapper.
//
// Reasons for using runpy.run_path rather than direct execution:
//   - Set the __main__ context so that if __name__ == '__main__' works.
//   - Forward sys.argv correctly (script path followed by user arguments).
//   - Catch SystemExit and write the exit code to a file.
//   - Use -u to disable Python output buffering for real-time output.
func (s *script) execPythonScript() (code int, err error) {
	// Pass the target path through XEXEC_TARGET rather than interpolating it into script text,
	// avoiding Python raw-string syntax errors for paths containing quotes or backslashes.
	wrapperContent := `# -*- coding: utf-8 -*-
import sys
import os
import runpy

exit_code = 0
script_path = os.environ['XEXEC_TARGET']

# Set sys.argv: the script path first, followed by the supplied arguments.
sys.argv = [script_path] + sys.argv[1:]

try:
    runpy.run_path(script_path, run_name='__main__')
except SystemExit as e:
    if e.code is None:
        exit_code = 0
    elif isinstance(e.code, int):
        exit_code = e.code
    else:
        # sys.exit("message"): print the message to stderr and normalize the exit code to 1.
        print(e.code, file=sys.stderr)
        exit_code = 1
except Exception:
    import traceback
    traceback.print_exc()
    exit_code = 255
finally:
    code_file = os.environ.get('XEXEC_EXIT_CODE_FILE')
    if code_file and not os.path.exists(code_file):
        try:
            with open(code_file, 'w') as f:
                f.write(str(exit_code if isinstance(exit_code, int) else 1))
        except:
            pass
    sys.exit(exit_code if isinstance(exit_code, int) else 1)
`
	wrapperPath := filepath.Join(os.TempDir(), s.randomFilename("wrapper", ".py"))
	defer func() {
		_ = os.Remove(wrapperPath)
	}()
	if err = os.WriteFile(wrapperPath, []byte(wrapperContent), os.ModePerm); err != nil {
		return 255, err
	}
	// Prefer python3, since many modern Linux/macOS systems no longer provide python;
	// fall back to python, then the Windows py launcher.
	python := "python3"
	for _, candidate := range []string{"python3", "python", "py"} {
		if _, lookErr := exec.LookPath(candidate); lookErr == nil {
			python = candidate
			break
		}
	}
	args := append([]string{"-u", wrapperPath}, s.args...)
	return s.exec(python, args...)
}
