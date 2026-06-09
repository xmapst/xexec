package xexec

import (
	"bytes"
	"io"
	"runtime/debug"
	"time"
)

// consoleOutput 异步读取脚本输出流（stdout 或 stderr），按行处理并输出到日志。
//
// 设计特点：
//  1. 独立 goroutine 读取数据到缓冲通道，解耦读取和处理，防止读取阻塞导致管道满
//  2. 按行切分输出：遇到 \n 时立即输出该行，保证实时性
//  3. 500ms 超时强制刷新：处理脚本输出不带换行符（如进度条）的情况，防止日志长时间不输出
//  4. 处理 Windows 的 \r\n 行尾：去除 \r 防止日志中出现乱码
//  5. 管道关闭时刷新缓冲区：确保最后一行没有换行符时也能输出
func (s *script) consoleOutput(title string, reader io.ReadCloser) {
	dataChan := make(chan []byte, 16)

	// 独立 goroutine 从管道读取原始字节数据，避免处理逻辑阻塞读取
	go func() {
		defer close(dataChan)          // 读取完毕时关闭通道，通知处理 goroutine 结束
		chunk := make([]byte, 64*1024) // 64KB 读取缓冲区
		for {
			n, err := reader.Read(chunk)
			if n > 0 {
				// 必须拷贝切片，原始 chunk 会被下次 Read 覆盖（数据竞争）
				tmp := make([]byte, n)
				copy(tmp, chunk[:n])

				select {
				case dataChan <- tmp:
				case <-s.ctx.Done():
					// 上下文取消且通道阻塞时，直接退出，防止 goroutine 泄漏
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

	// flushLine 处理单行输出：编码转换（GBK→UTF8）→ 日志脱敏 → 输出
	flushLine := func(lineBytes []byte) {
		line := string(lineBytes)
		line = s.transform(line) // 平台特定的字符编码转换
		if s.masker != nil {
			line = s.masker.Replace(line) // 替换敏感词为 "***"
		}
		s.logger.Infof("[%s] %s", title, line)
	}

	// 提取公共的按行处理逻辑
	processBuf := func() {
		for {
			if buf.Len() == 0 {
				break
			}

			// 使用 bytes.Cut 替代 bytes.IndexByte，避免某些 go fix 版本 panic
			line, _, found := bytes.Cut(buf.Bytes(), []byte{'\n'})
			if !found {
				break // 没有完整的行，等待更多数据
			}

			// 获取完整一行（包含 \n）的长度，并从缓冲区中消费掉这部分数据
			consumeLen := len(line) + 1
			lineBytes := buf.Next(consumeLen)
			lineBytes = lineBytes[:len(lineBytes)-1] // 去除 \n

			// 去除 Windows 行尾的 \r（Carriage Return）
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
				// 通道关闭，说明读取 goroutine 已结束，刷新缓冲区中残留的最后一行
				if buf.Len() > 0 {
					flushLine(buf.Bytes())
				}
				return
			}

			buf.Write(data)
			processBuf()

		case <-ticker.C:
			// 超时强制刷新：防止脚本输出不带换行符时日志长时间没有输出（如进度条场景）
			if buf.Len() > 0 {
				flushLine(buf.Bytes())
				buf.Reset()
			}

		case <-s.ctx.Done():
			// 上下文取消（超时或手动取消），尽量消费掉通道内残余的数据
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

			// 处理完积压数据后，按行提取输出
			processBuf()

			// 将没有换行符的最后一部分也强行输出
			if buf.Len() > 0 {
				flushLine(buf.Bytes())
			}
			return
		}
	}
}
