package xexec

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"strings"
)

const varNameExpr = `[A-Za-z_][A-Za-z0-9_-]*`

var (
	// variablePlaceholder matches several variable placeholder styles.
	//
	// Supported formats:
	//   - %VAR%: custom style, capture group 1
	//   - ${VAR}: Shell brace style, capture group 2
	//   - $VAR: Shell shorthand, capture group 3
	//   - {{VAR}} or {{ VAR }}: template style (inner whitespace allowed), capture group 4
	//   - {VAR} or { VAR }: template style (inner whitespace allowed), capture group 5
	//
	// Variable names must start with a letter or underscore ([A-Za-z_]);
	// subsequent characters may include letters, digits, underscores, and hyphens ([A-Za-z0-9_-]).
	placeholderPatterns = []string{
		`%(` + varNameExpr + `)%`,
		`\$\{(` + varNameExpr + `)\}`,
		`\$(` + varNameExpr + `)`,
		`\{\{\s*(` + varNameExpr + `)\s*\}\}`,
		`\{\s*(` + varNameExpr + `)\s*\}`,
	}
	variablePlaceholder = regexp.MustCompile(strings.Join(placeholderPatterns, "|"))
)

// Inject replaces variable placeholders in the content string.
//
// Lookup precedence: the envs argument, then system environment variables (os.LookupEnv).
// If neither source contains the variable, its placeholder is left unchanged,
// except for whitespace normalization in template-style placeholders. A nil envs map is safe.
func Inject(content string, envs map[string]any) string {
	if envs == nil {
		envs = make(map[string]any)
	}

	// lookup checks envs first, then the system environment.
	lookup := func(name string) (string, bool) {
		if val, ok := envs[name]; ok {
			return fmt.Sprintf("%v", val), true
		}
		if val, exists := os.LookupEnv(name); exists {
			return val, true
		}
		return "", false
	}

	content = variablePlaceholder.ReplaceAllStringFunc(content, func(match string) string {
		submatches := variablePlaceholder.FindStringSubmatch(match)
		// The first nonempty capture group is the variable name; only one style can match.
		// Capture group indices: 1=%VAR%, 2=${VAR}, 3=$VAR, 4={{VAR}}, 5={VAR}
		var name string
		var groupIdx int
		for i := 1; i < len(submatches); i++ {
			if submatches[i] != "" {
				name = submatches[i]
				groupIdx = i
				break
			}
		}

		if name == "" {
			return match
		}

		if val, found := lookup(name); found {
			return val
		}

		// Variable not found: normalize whitespace inside {{ VAR }} / { VAR }; leave other styles unchanged.
		switch groupIdx {
		case 4:
			return "{{" + name + "}}"
		case 5:
			return "{" + name + "}"
		default:
			return match
		}
	})

	return content
}

// InjectArgs replaces variable placeholders in command-line arguments.
func InjectArgs(args []string, envs map[string]any) []string {
	for i, arg := range args {
		args[i] = Inject(arg, envs)
	}
	return args
}

// InjectFile injects environment variables into a single file at the specified path.
// It reads the file, replaces placeholders, and writes back to the file.
// Rollback support: write new files first, and overwrite files only if there are no exceptions.
// If the file does not exist, it returns nil.
func InjectFile(path string, envs map[string]any) (err error) {
	var info fs.FileInfo
	info, err = os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			// If the file does not exist, return nil.
			return nil
		}
		return &Error{Op: "stat injection file", Path: path, Err: err}
	}

	if info.Size() == 0 {
		// If the file is empty, return nil
		return
	}

	// Open the file for reading
	inFile, err := os.Open(path)
	if err != nil {
		return &Error{Op: "open injection file", Path: path, Err: err}
	}
	defer func() {
		_ = inFile.Close()
	}()

	var tempPath = path + ".tmp"
	defer func() {
		_ = os.Remove(tempPath)
	}()

	// Create a temporary file for writing
	tmpFile, err := os.OpenFile(tempPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, info.Mode())
	if err != nil {
		return &Error{Op: "create injection temporary file", Path: tempPath, Err: err}
	}
	defer func() {
		_ = tmpFile.Close()
	}()

	// Read the file line by line
	const bufferSize = 64 * 1024
	buf := make([]byte, bufferSize)
	scanner := bufio.NewScanner(inFile)
	scanner.Buffer(buf, bufferSize)
	writer := bufio.NewWriter(tmpFile)
	hasChanges := false
	for scanner.Scan() {
		line := scanner.Text()
		// Replace placeholders in the line
		newLine := Inject(line, envs)
		if newLine != line {
			// Mark that changes were made
			hasChanges = true
		}
		// Write the modified line to the temporary file
		_, err = fmt.Fprintln(writer, newLine)
		if err != nil {
			return &Error{Op: "write injection file", Path: tempPath, Err: err}
		}
	}
	if err = scanner.Err(); err != nil {
		return &Error{Op: "read injection file", Path: path, Err: err}
	}

	// Flush the writer to ensure all data is written
	if err = writer.Flush(); err != nil {
		return &Error{Op: "flush injection file", Path: tempPath, Err: err}
	}
	if !hasChanges {
		// No changes made, do not overwrite the original file
		return
	}
	// Windows cannot replace files while either handle is open without delete sharing.
	// Close both handles before renaming; deferred closes still cover error paths.
	if err = inFile.Close(); err != nil {
		return &Error{Op: "close injection input file", Path: path, Err: err}
	}
	if err = tmpFile.Close(); err != nil {
		return &Error{Op: "close injection temporary file", Path: tempPath, Err: err}
	}
	if err = os.Rename(tempPath, path); err != nil {
		return &Error{Op: "replace injection file", Path: path, Value: tempPath, Err: err}
	}
	return
}
