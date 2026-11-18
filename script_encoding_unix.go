//go:build linux || darwin

package xexec

// utf8ToGb2312 is a no-op outside Windows; Linux/macOS use UTF-8 by default.
func (s *script) utf8ToGb2312(line string) string {
	return line
}

// transform returns the original string without encoding conversion outside Windows.
func (s *script) transform(line string) string {
	return line
}
