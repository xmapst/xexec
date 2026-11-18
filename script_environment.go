package xexec

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// mergeEnv merges system and custom environment variables; custom values take precedence.
//
// First collect system variables in a map, then override matching keys with custom values.
// Finally append the built-in target path, exit-code file, and exported-environment file variables
// so the wrapper can locate the script and write its exit code and exported variables.
func (s *script) mergeEnv() []string {
	envMap := make(map[string]string)

	// Load system environment variables as the baseline.
	for _, env := range os.Environ() {
		key := s.getEnvKey(env)
		if key != "" {
			envMap[key] = env
		}
	}

	// Custom values override system variables with the same name.
	for _, env := range s.env {
		key := s.getEnvKey(env)
		if key != "" {
			envMap[key] = env
		}
	}

	result := make([]string, 0, len(envMap))
	for _, env := range envMap {
		result = append(result, env)
	}

	// Inject built-in variables:
	//   - XEXEC_TARGET: the absolute target path, read by the wrapper from the environment,
	//     avoiding escaping and injection issues from interpolating paths into script text.
	//     Quotes, spaces, backslashes, and Chinese characters in paths are safe.
	//   - XEXEC_EXIT_CODE_FILE / XEXEC_SAVE_ENV_FILE: the script uses these variables
	//     to locate the files for its exit code and exported environment variables.
	result = append(
		result,
		fmt.Sprintf("XEXEC_TARGET=%s", s.path),
		fmt.Sprintf("XEXEC_EXIT_CODE_FILE=%s", s.exitCodePath),
		fmt.Sprintf("XEXEC_SAVE_ENV_FILE=%s", s.saveEnvPath),
	)

	return result
}

// getEnvKey extracts the key from a KEY=VALUE environment string.
func (s *script) getEnvKey(env string) string {
	key, _, found := strings.Cut(env, "=")
	if !found {
		return ""
	}
	return key
}

// parseSaveEnvFile parses the environment file exported through XEXEC_SAVE_ENV_FILE.
//
// Two file formats are supported:
//   - Single line: KEY=VALUE
//   - Multiline: KEY<<DELIMITER\n...\nDELIMITER (like a Bash here-document)
//
// Safety: scripts cannot override existing system environment variables (checked with os.LookupEnv),
// preventing changes to process-level variables such as PATH and HOME through this mechanism.
func (s *script) parseSaveEnvFile() map[string]any {
	var saveEnv = make(map[string]any)
	file, err := os.Open(s.saveEnvPath)
	if err != nil {
		return saveEnv
	}
	defer func() {
		_ = file.Close()
	}()

	scanner := bufio.NewScanner(file)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 10*1024*1024) // Allow lines up to 10 MB for large JSON values.

	firstLine := true
	for scanner.Scan() {
		line := scanner.Text()

		// Handle UTF-8 BOMs inserted at the beginning of files by some Windows editors.
		if firstLine {
			firstLine = false
			if len(line) >= 3 && line[0] == 0xEF && line[1] == 0xBB && line[2] == 0xBF {
				s.logger.Debugf("[SYSTEM] UTF-8 BOM detected and removed")
				line = line[3:]
			}
		}

		if strings.TrimSpace(line) == "" {
			continue
		}

		// Choose single-line or multiline parsing based on the relative positions of << and =.
		// Case A: only = (no <<) -> single line.
		// Case B: = before << -> single line (<< in the value is ordinary text).
		// Case C: << before = (or no =) -> multiline here-document.
		multiKey, multiRest, hasMulti := strings.Cut(line, "<<")
		singleKey, singleValue, hasSingle := strings.Cut(line, "=")

		isMultiLine := hasMulti && (!hasSingle || len(multiKey) < len(singleKey))

		if isMultiLine {
			key := strings.TrimSpace(multiKey)
			delimiter := strings.TrimSpace(multiRest)

			if key == "" || delimiter == "" {
				s.logger.Errorf("[SYSTEM] invalid multiline format: key or delimiter is empty in line '%s'", line)
				continue
			}

			var valueBuilder strings.Builder
			delimiterFound := false

			for scanner.Scan() {
				content := scanner.Text()
				if strings.TrimSpace(content) == delimiter {
					delimiterFound = true
					break
				}
				if valueBuilder.Len() > 0 {
					valueBuilder.WriteString("\n")
				}
				valueBuilder.WriteString(content)
			}

			if !delimiterFound {
				s.logger.Errorf("[SYSTEM] EOF reached before finding delimiter '%s'", delimiter)
				return saveEnv
			}

			// Disallow overwriting system environment variables to protect the process environment.
			if _, exists := os.LookupEnv(key); exists {
				s.logger.Warnf("[SYSTEM] ignored attempt to overwrite system variable: %s", key)
				continue
			}
			saveEnv[key] = valueBuilder.String()
			s.logger.Debugf("[SYSTEM] parsed env: %s", key)

		} else if hasSingle {
			key := strings.TrimSpace(singleKey)

			if key == "" {
				s.logger.Errorf("[SYSTEM] invalid format: empty key in line '%s'", line)
				continue
			}

			if _, exists := os.LookupEnv(key); exists {
				s.logger.Warnf("[SYSTEM] ignored attempt to overwrite system variable: %s", key)
				continue
			}
			saveEnv[key] = singleValue
			s.logger.Debugf("[SYSTEM] parsed env: %s", key)

		} else {
			s.logger.Errorf("[SYSTEM] invalid format '%v', expected 'KEY=VALUE' or 'KEY<<EOF'", line)
			continue
		}
	}

	if err = scanner.Err(); err != nil {
		s.logger.Errorf("[SYSTEM] error reading file: %v", err)
		return saveEnv
	}

	s.logger.Infof("[SYSTEM] successfully parsed %d environment variables.", len(saveEnv))
	return saveEnv
}
