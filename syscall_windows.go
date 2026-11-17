//go:build windows

package xexec

import (
	"fmt"
	"os/exec"
	"strconv"
)

// KillAll 在 Windows 上通过 TASKKILL /T /F 递归终止指定 PID 的进程树（Windows 实现）。
// 使用 TASKKILL 命令的原因：Windows 没有 Unix 进程组的概念，
// Process.Kill() 仅能终止直接进程，无法终止其派生的子进程；
// TASKKILL /T 选项可递归终止整个进程树，/F 强制终止（不等待进程自行退出）。
// 非正 PID 不允许传入，避免对系统进程误操作。
func KillAll(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("kill all: invalid pid %d", pid)
	}
	kill := exec.Command("TASKKILL.exe", "/T", "/F", "/PID", strconv.Itoa(pid))
	return kill.Run()
}
