//go:build !windows

package xexec

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"syscall"
)

// execSysScript 在 Unix/Linux 平台上通过 sh 包装脚本执行目标脚本。
//
// 包装脚本的作用：
//  1. 通过 trap 捕获 EXIT/INT/TERM 信号，确保无论何种原因退出都会写入退出码文件
//  2. 以 "$@" 方式传递参数，正确处理含空格的参数
//  3. 条件写入（[ ! -f ]）防止重复写入覆盖已有的退出码
func (s *script) execSysScript() (code int, err error) {
	_ = os.Chmod(s.path, os.ModePerm)
	wrapperContent := fmt.Sprintf(`#!/bin/sh
_save_exit_code() {
	local code=$?
	[ ! -f "${XEXEC_EXIT_CODE_FILE}" ] && echo $code > "${XEXEC_EXIT_CODE_FILE}"
	exit $code
}
trap _save_exit_code EXIT INT TERM
"%s" "$@"
`, s.path)

	wrapperPath := filepath.Join(os.TempDir(), s.randomFilename("wrapper", ".sh"))
	defer func() {
		_ = os.Remove(wrapperPath)
	}()

	if err = os.WriteFile(wrapperPath, []byte(wrapperContent), os.ModePerm); err != nil {
		return 255, fmt.Errorf("failed to create wrapper script: %w", err)
	}

	args := append([]string{wrapperPath}, s.args...)
	return s.exec("sh", args...)
}

// beforeExec 在 Unix 平台上配置进程属性。
//
// 设置新进程组（Setpgid=true）的目的：
//   - 使子进程和其派生的所有子进程形成独立进程组
//   - 取消时通过 Kill(-pid, SIGKILL) 向整个进程组发送信号，确保所有子进程都被终止
//   - 防止孤儿进程（orphan process）残留
//
// cmd.Cancel 钩子在上下文取消时调用，实现级联终止整个进程树。
func (s *script) beforeExec() {
	s.cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
		Pgid:    os.Getpid(),
	}

	s.cmd.Cancel = func() error {
		defer func() {
			if r := recover(); r != nil {
				s.logger.Errorf("[SYSTEM] cmd cancel panic: %v\n%s", r, string(debug.Stack()))
			}
			// 写入 137（128 + SIGKILL 信号编号 9）作为标准的被杀死退出码
			_ = os.WriteFile(s.exitCodePath, []byte("137"), os.ModePerm)
		}()

		if s.cmd.Process == nil {
			return fmt.Errorf("process not started")
		}

		var errs error
		pid := s.cmd.Process.Pid

		// 先终止进程本身
		if err := s.cmd.Process.Kill(); err != nil {
			errs = errors.Join(errs, fmt.Errorf("kill process: %w", err))
		}

		// 再向整个进程组发送 SIGKILL，确保子进程也被终止（-pid 表示进程组 ID）
		if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
			errs = errors.Join(errs, fmt.Errorf("kill process group: %w", err))
		}

		if errs != nil {
			return fmt.Errorf("error cancelling pid=%d: %w", pid, errs)
		}
		return nil
	}
}

// utf8ToGb2312 在非 Windows 平台上为空操作，Linux/macOS 默认使用 UTF-8。
func (s *script) utf8ToGb2312(line string) string {
	return line
}

// transform 在非 Windows 平台上直接返回原始字符串，不进行编码转换。
func (s *script) transform(line string) string {
	return line
}
