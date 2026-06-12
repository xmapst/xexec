//go:build linux || freebsd

package xexec

import "syscall"

// sysProcAttr 在支持 Pdeathsig 的平台（Linux、FreeBSD）上构建进程属性。
//
// 在 Setpgid（新进程组，便于 Kill(-pid) 清理进程树）基础上，额外设置
// Pdeathsig=SIGKILL：当父进程死亡时，内核自动向子进程发送 SIGKILL。
// 这覆盖了 cmd.Cancel 钩子无法处理的场景——Go 进程崩溃、被 kill -9、
// panic 直接退出时 defer cancel() 来不及执行，子进程会变成孤儿存活。
//
// 职责边界（重要，别误解它的能力）：
//   - 退出码的可靠性与 Pdeathsig 无关，由 wrapper 的 trap + 退出码文件保证
//     （见 execSysScript 注释）。Pdeathsig 只负责进程清理，不参与退出码。
//   - Pdeathsig 是“单进程级”信号，只清理 Go 的直接子进程（wrapper sh）。
//     wrapper 或目标脚本自身 fork 出的孙进程（如脚本里的 cmd &）够不到，
//     父崩溃时仍会残留。要清理整棵进程树需按进程组 Kill(-pgid)，但
//     Pdeathsig 无法对进程组生效；真正根治需独立的 reaper 守护进程。
//     正常 cancel/超时路径下整树清理由 cmd.Cancel 的 Kill(-pid) 负责，不受此限。
//
// 实现说明：Pdeathsig 监听的是“创建子进程的那个 OS 线程”退出，而非整个 Go
// 进程退出。本库未配合 runtime.LockOSThread——锁线程需在脚本执行全程独占一个
// OS 线程，对长时间脚本是明显浪费；而 Go runtime 实践中几乎不回收已创建的 M，
// 故进程仍存活时该线程先退出进而误杀子进程的概率可忽略。
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Setpgid:   true,
		Pdeathsig: syscall.SIGKILL,
	}
}
