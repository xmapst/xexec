package xexec

import (
	"bytes"
	"io"
	"runtime/debug"
	"time"
)

// consoleOutput asynchronously reads stdout or stderr and logs it line by line.
//
// Design:
//  1. A dedicated goroutine reads into a buffered channel, separating reads from processing.
//  2. Emit each line as soon as a newline arrives for real-time output.
//  3. Flush every 500 ms for output without newlines, such as progress bars.
//  4. Strip carriage returns from Windows CRLF line endings.
//  5. Flush when the pipe closes, including a final line without a newline.
func (s *script) consoleOutput(title string, reader io.ReadCloser) {
	dataChan := make(chan []byte, 16)

	// Read raw pipe bytes in a dedicated goroutine so processing does not block reads.
	go func() {
		defer close(dataChan)          // Close the channel when reading finishes to notify the processing goroutine.
		chunk := make([]byte, 64*1024) // 64 KB read buffer
		for {
			n, err := reader.Read(chunk)
			if n > 0 {
				// Copy the slice: the next Read overwrites chunk, which would cause a data race.
				tmp := make([]byte, n)
				copy(tmp, chunk[:n])

				select {
				case dataChan <- tmp:
				case <-s.ctx.Done():
					// Exit on cancellation if the channel blocks, preventing a goroutine leak.
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	defer func() {
		if r := recover(); r != nil {
			s.logger.Errorf("[SYSTEM] %s panic:%v\n%s", title, r, string(debug.Stack()))
		}
		s.logger.Infof("[SYSTEM] stop %s console print", title)
	}()

	var buf bytes.Buffer
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	// flushLine converts encoding (GBK -> UTF-8), redacts secrets, and logs one line.
	flushLine := func(lineBytes []byte) {
		line := string(lineBytes)
		line = s.transform(line) // Platform-specific character encoding conversion
		if s.masker != nil {
			line = s.masker.Replace(line) // Replace sensitive strings with "***".
		}
		s.logger.Infof("[%s] %s", title, line)
	}

	// Shared line-by-line processing logic
	processBuf := func() {
		for {
			if buf.Len() == 0 {
				break
			}

			// Use bytes.Cut rather than bytes.IndexByte to avoid panics in some go fix versions.
			line, _, found := bytes.Cut(buf.Bytes(), []byte{'\n'})
			if !found {
				break // No complete line yet; wait for more data.
			}

			// Consume the complete line, including its newline, from the buffer.
			consumeLen := len(line) + 1
			lineBytes := buf.Next(consumeLen)
			lineBytes = lineBytes[:len(lineBytes)-1] // Remove the newline.

			// Remove the carriage return from Windows line endings.
			if len(lineBytes) > 0 && lineBytes[len(lineBytes)-1] == '\r' {
				lineBytes = lineBytes[:len(lineBytes)-1]
			}
			flushLine(lineBytes)
		}
	}

	for {
		select {
		case data, ok := <-dataChan:
			if !ok {
				// The reader has finished; flush any remaining final line.
				if buf.Len() > 0 {
					flushLine(buf.Bytes())
				}
				return
			}

			buf.Write(data)
			processBuf()

		case <-ticker.C:
			// Flush periodically to keep output without newlines (such as progress bars) visible.
			if buf.Len() > 0 {
				flushLine(buf.Bytes())
				buf.Reset()
			}

		case <-s.ctx.Done():
			// On cancellation or timeout, drain as much remaining channel data as possible.
		drainLoop:
			for {
				select {
				case data, ok := <-dataChan:
					if !ok {
						break drainLoop
					}
					buf.Write(data)
				default:
					break drainLoop
				}
			}

			// Process the drained data line by line.
			processBuf()

			// Also emit the final fragment even if it has no newline.
			if buf.Len() > 0 {
				flushLine(buf.Bytes())
			}
			return
		}
	}
}
