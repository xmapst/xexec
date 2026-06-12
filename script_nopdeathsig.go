//go:build !windows && !linux && !freebsd

package xexec

import "syscall"

// sysProcAttr 在不支持 Pdeathsig 的 Unix 平台（macOS、OpenBSD、NetBSD、
// dragonfly、solaris 等）上构建进程属性。
//
// 这些平台的 syscall.SysProcAttr 没有 Pdeathsig 字段，无法实现内核级的
// 父死子死。仅设 Setpgid：子进程成为新进程组组长，cancel 中的
// Kill(-pid, SIGKILL) 可终止整个进程组。Go 进程异常退出（崩溃 / kill -9）
// 时子进程仍可能变成孤儿存活——这与本库改进前在所有平台上的行为一致。
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Setpgid: true,
	}
}
