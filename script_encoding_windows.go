//go:build windows

package xexec

import (
	"io"
	"runtime/debug"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

var (
	// acp caches the system's active code page, read once at package initialization.
	// 936 = GBK/GB2312, the default code page on Chinese Windows systems.
	acp = windows.GetACP()
)

// transform handles character encoding conversion on Windows.
//
// If the system code page is GBK (936), or output appears to contain GBK characters,
// convert it to UTF-8 to avoid garbled Chinese logs.
func (s *script) transform(line string) string {
	if acp == 936 || s.isGBK(line) {
		return s.gbkToUtf8(line)
	}
	return line
}

// gbkToUtf8 converts a GBK-encoded string to UTF-8.
//
// Use simplifiedchinese.GBK from golang.org/x/text
// with transform.NewReader for streaming decoding rather than allocating it all at once.
func (s *script) gbkToUtf8(line string) string {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Errorf("[SYSTEM] gbkToUtf8 panic: %v\n%s", r, string(debug.Stack()))
		}
	}()

	reader := transform.NewReader(strings.NewReader(line), simplifiedchinese.GBK.NewDecoder())
	b, err := io.ReadAll(reader)
	if err != nil {
		s.logger.Errorf("[SYSTEM] gbkToUtf8 error: %v", err)
		return line
	}
	return string(b)
}

// isGBK detects whether a string contains GBK-encoded multibyte characters.
//
// GBK double-byte encoding rules:
//   - First byte: 0x81 through 0xFE.
//   - Second byte: 0x40 through 0xFE, excluding 0x7F.
//
// Algorithm:
//   - Skip ASCII characters (<= 0x7F), which do not affect detection.
//   - Require at least one valid GBK double-byte sequence to avoid matching pure ASCII.
//   - Return false immediately for bytes that violate GBK rules.
func (s *script) isGBK(data string) bool {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Errorf("[SYSTEM] isGBK panic: %v\n%s", r, string(debug.Stack()))
		}
	}()

	length := len(data)
	hasMultiByte := false

	for i := 0; i < length; {
		b := data[i]

		if b <= 0x7f {
			i++
			continue
		}

		// Bounds check: a GBK double-byte character requires two bytes.
		if i+1 >= length {
			return false
		}

		b2 := data[i+1]
		// Validate the GBK double-byte character.
		if b >= 0x81 && b <= 0xfe && b2 >= 0x40 && b2 <= 0xfe && b2 != 0x7f {
			hasMultiByte = true
			i += 2
			continue
		}

		// Invalid GBK byte sequence.
		return false
	}

	// Do not treat pure ASCII as GBK, avoiding unnecessary conversion.
	return hasMultiByte
}
