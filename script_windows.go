//go:build windows

package xexec

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

var (
	// acp 系统活动代码页（Active Code Page），在包加载时获取一次并缓存。
	// 936 = GBK/GB2312，中文 Windows 系统的默认代码页。
	acp = windows.GetACP()
)

// execSysScript 在 Windows 平台上根据文件扩展名选择 PowerShell 或 cmd.exe 执行脚本。
//
// PowerShell 包装脚本特点：
//   - -NonInteractive 防止脚本等待用户输入
//   - -ExecutionPolicy Bypass 绕过执行策略限制，确保脚本能运行
//   - 捕获异常并写入退出码文件，$null 退出码规范化为 0
//
// cmd.exe 包装脚本特点：
//   - @echo off 禁止命令回显
//   - %%* 透传所有参数（cmd 语法）
//   - 条件写入退出码文件防止覆盖
func (s *script) execSysScript() (code int, err error) {
	ext := filepath.Ext(s.path)
	var (
		command        string
		args           []string
		wrapperPath    string
		wrapperContent string
	)
	switch ext {
	case ".ps1", ".ps":
		command = "powershell.exe"
		args = []string{"-NoLogo", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File"}
		wrapperPath = filepath.Join(os.TempDir(), s.randomFilename("wrapper", ".ps1"))
		wrapperContent = fmt.Sprintf(`$ErrorActionPreference='Continue'
try {
    & "%s" @args
    if ($LASTEXITCODE -eq $null) {
        $exitCode = 0
    } else {
        $exitCode = $LASTEXITCODE
    }
} catch {
    $exitCode = 255
    Write-Error "[$($_.Exception.GetType().FullName)] $($_.Exception.Message)"
} finally {
    if (-not (Test-Path "$env:XEXEC_EXIT_CODE_FILE")) {
        $exitCode | Out-File -FilePath "$env:XEXEC_EXIT_CODE_FILE" -Encoding ASCII
    }
    exit $exitCode
}`, s.path)
	default:
		command = "cmd.exe"
		args = []string{"/C"}
		wrapperPath = filepath.Join(os.TempDir(), s.randomFilename("wrapper", ".bat"))
		wrapperContent = fmt.Sprintf(`@echo off
call "%s" %%*
set exitcode=%%ERRORLEVEL%%
if not exist "%%XEXEC_EXIT_CODE_FILE%%" (
    echo %%exitcode%% > "%%XEXEC_EXIT_CODE_FILE%%"
)
exit /b %%exitcode%%`, s.path)
	}
	defer func() {
		_ = os.Remove(wrapperPath)
	}()
	if err = os.WriteFile(wrapperPath, []byte(wrapperContent), os.ModePerm); err != nil {
		return 255, err
	}
	args = append(args, wrapperPath)
	args = append(args, s.args...)
	return s.exec(command, args...)
}

// beforeExec 在 Windows 平台上配置进程属性。
//
// CREATE_NEW_PROCESS_GROUP 创建新进程组，使子进程与父进程隔离，
// 便于通过 TASKKILL /T 递归终止整个进程树（包括子进程的子进程）。
// HideWindow=true 防止在服务器环境中弹出控制台窗口影响用户体验。
//
// cmd.Cancel 使用 TASKKILL.exe 的 /T（树）和 /F（强制）参数
// 确保整个进程树都被终止，而不仅仅是直接子进程。
func (s *script) beforeExec() {
	s.cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP, // 创建独立进程组
		HideWindow:    true,                             // 隐藏控制台窗口
	}
	s.cmd.Cancel = func() error {
		defer func() {
			if r := recover(); r != nil {
				s.logger.Errorf("[SYSTEM] cmd cancel panic: %v\n%s", r, string(debug.Stack()))
			}
			_ = os.WriteFile(s.exitCodePath, []byte("137"), os.ModePerm)
		}()
		if s.cmd.Process == nil {
			return fmt.Errorf("process not started")
		}
		var errs error
		if err := s.cmd.Process.Kill(); err != nil {
			errs = errors.Join(errs, err)
		}
		// TASKKILL /T 递归终止进程树，/F 强制终止（不等待进程响应）
		kill := exec.Command("TASKKILL.exe", "/T", "/F", "/PID", strconv.Itoa(s.cmd.Process.Pid))
		if err := kill.Run(); err != nil {
			errs = errors.Join(errs, err)
		}
		if errs != nil {
			return fmt.Errorf("error cancelling pid=%d, errors=%v", s.cmd.Process.Pid, errs)
		}
		return nil
	}
}

// transform 在 Windows 平台上处理字符编码转换。
//
// 当系统代码页为 GBK（936）或检测到输出包含 GBK 编码字符时，
// 自动转换为 UTF-8，防止中文日志出现乱码。
func (s *script) transform(line string) string {
	if acp == 936 || s.isGBK(line) {
		return s.gbkToUtf8(line)
	}
	return line
}

// gbkToUtf8 将 GBK 编码字符串转换为 UTF-8。
//
// 使用 golang.org/x/text 库的 simplifiedchinese.GBK 编码器，
// 通过 transform.NewReader 实现流式解码，比一次性分配更高效。
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

// isGBK 检测字符串是否包含 GBK 编码的多字节字符。
//
// GBK 双字节编码规则：
//   - 第一字节范围：0x81~0xFE
//   - 第二字节范围：0x40~0xFE（排除 0x7F）
//
// 算法特点：
//   - ASCII 字符（≤ 0x7F）直接跳过，不影响判断
//   - 只有包含至少一个合法 GBK 双字节序列时才返回 true（避免纯 ASCII 误判）
//   - 遇到不符合 GBK 规则的字节立即返回 false（快速失败）
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

		// 边界检查：GBK 双字节需要至少 2 个字节
		if i+1 >= length {
			return false
		}

		b2 := data[i+1]
		// 验证 GBK 双字节字符的合法性
		if b >= 0x81 && b <= 0xfe && b2 >= 0x40 && b2 <= 0xfe && b2 != 0x7f {
			hasMultiByte = true
			i += 2
			continue
		}

		// 不符合 GBK 编码规则，判定为非 GBK
		return false
	}

	// 纯 ASCII 不视为 GBK，避免不必要的编码转换
	return hasMultiByte
}
