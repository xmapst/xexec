//go:build !windows

package xexec

import (
	"errors"
	"fmt"
	"syscall"
)

// KillAll 终止指定 PID 对应的独立进程组（受管进程的 PGID=PID）。
// 即使组长已经退出，也先清理同组后代；主动 setsid 脱组的后代不在范围内。
// 若组不存在，退回终止单个进程，兼容没有创建独立进程组的调用方。
// ESRCH 视为幂等成功，其他信号错误必须返回。pid <= 1 不允许传入，
// 避免将 -pid 作为信号目标时触发特殊语义，或误变为对单个进程发送信号。
func KillAll(pid int) error {
	if pid <= 1 {
		return fmt.Errorf("kill all: invalid pid %d", pid)
	}
	// 将信号发送与终止策略分开，以便验证权限等错误分支。
	groupErr := syscall.Kill(-pid, syscall.SIGKILL)
	if groupErr == nil {
		// 主进程就在此组中，不再重复按 PID 发信号，避免退出后的 PID 重用。
		return nil
	}
	var errs error
	if !errors.Is(groupErr, syscall.ESRCH) {
		errs = fmt.Errorf("kill process group %d: %w", pid, groupErr)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		errs = errors.Join(errs, fmt.Errorf("kill process %d: %w", pid, err))
	}
	return errs
}
