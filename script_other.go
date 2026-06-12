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
//
// 为什么用 wrapper + trap 而不是让 sh 直接 exec 目标脚本：
// exec 会用目标进程映像替换 sh，之后不再有任何代码能运行，退出码就只能
// 依赖 Go 侧 cmd.Wait()/ProcessState 获取。但 xexec 是库，无法控制宿主进程
// 的 SIGCHLD 处置——若宿主把 SIGCHLD 设为 SIG_IGN 或自带 wait(-1) 的
// reaper，子进程可能在 Go 调用 wait4 之前就被抢先回收，导致 ECHILD
// （exec() 中特判的 "waitid: no child processes"），退出码永久丢失退化为 255。
// trap 让 sh 留在原地、在目标结束后把真实退出码写入文件，该文件由脚本自己
// 写入、不依赖谁来 wait()，因此对外部 reaper 抢收免疫。这是刻意的健壮性取舍，
// 不要为了少一层进程而改成 exec。
func (s *script) execSysScript() (code int, err error) {
	_ = os.Chmod(s.path, os.ModePerm)
	// 目标脚本路径通过 XEXEC_TARGET 环境变量传入，而非插值进脚本文本，
	// 避免路径含特殊字符时破坏 shell 引号或构成命令注入。
	// 不使用 local（非 POSIX）；printf 替代 echo 以规避实现差异。
	wrapperContent := `#!/bin/sh
_save_exit_code() {
	code=$?
	[ ! -f "${XEXEC_EXIT_CODE_FILE}" ] && printf '%s\n' "$code" > "${XEXEC_EXIT_CODE_FILE}"
	exit $code
}
trap _save_exit_code EXIT INT TERM
"${XEXEC_TARGET}" "$@"
`

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
	// SysProcAttr 按平台构建（见 sysProcAttr）：
	//   - 所有 Unix 平台都设 Setpgid，使子进程成为新进程组组长，
	//     cancel 中的 Kill(-pid, SIGKILL) 即可终止整个进程组。
	//   - Linux/FreeBSD 额外设 Pdeathsig=SIGKILL，使 Go 进程异常退出
	//     （崩溃 / kill -9，defer cancel 来不及执行）时内核自动清理子进程。
	s.cmd.SysProcAttr = sysProcAttr()

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
