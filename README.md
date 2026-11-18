# xexec

`xexec` 是一个对 Go 标准库 [`os/exec`](https://pkg.go.dev/os/exec) 的增强封装，统一提供异步命令执行、跨平台脚本执行、进程组终止和 Linux PID 1 孤儿回收能力。

该项目重点解决以下问题：

- 跨平台执行不同类型脚本：Shell、Batch、PowerShell、Python
- 获取更可靠的真实退出码，而不是仅依赖进程状态
- 实时读取并输出标准输出 / 标准错误
- 支持超时、取消、整棵进程树清理
- 支持脚本向 Go 侧“回传环境变量”
- 支持日志脱敏与自定义日志实现
- 支持模板变量注入
- 兼顾 Windows 中文编码场景，尽量避免乱码

## 异步命令执行

`ExecContext(ctx, command, options...)` 直接启动程序并返回 `*XCmd`，调用方必须调用 `Wait()`。通过 `WithExecArgs(...)` 传入独立参数；Windows 不再隐式拼接为 `cmd.exe /C` 命令文本，需要 shell 内建命令或管道时请显式调用 `cmd.exe` 并传入 `/C`（此时调用方负责 shell 文本的安全性）。

每次执行在临时目录创建独立的 console 日志，stdout/stderr 共用该实例的锁与轮转状态，默认上限为 50 MiB，不会与相同命令的其他实例混写。

默认 `WaitDelay` 为 250ms，限制根进程退出或 context 取消后的输出排空等待。后台后代持有输出管道且超限时，`Wait()` 返回等待错误；对于 `exec.ErrWaitDelay`，还会尝试清理剩余进程组。Unix 可在组长退出后终止同组后代；Windows 使用原生进程快照和进程句柄清理后代；根进程已退出、并发创建新后代或 PID 复用时，不保证清理整棵树，清理错误会保留在返回值中。

## 功能特性

### 1. 多脚本类型执行

核心入口 [`ExecScript()`](script.go:72) 会根据脚本扩展名自动选择执行方式：

- Python 脚本：`.py`、`.py3`
- Windows 脚本：`.ps1`、`.ps`、`.bat` 等
- Unix/Linux Shell 脚本：如 `.sh`

其中：

- Python 使用包装器脚本配合 `runpy.run_path()` 执行，保证 `if __name__ == '__main__'` 语义正确；解释器按 `python3` → `python` → `py` 顺序自动探测，适配只提供 `python3` 的现代系统
- Windows 下自动区分 `powershell.exe` 与 `cmd.exe`
- 非 Windows 平台统一走 `sh` 包装执行
- 目标脚本路径通过内部环境变量 `XEXEC_TARGET` 传入包装脚本，而非拼接进脚本文本，避免路径含引号、空格、反斜杠、中文等字符时破坏语法或构成命令注入

实现位置可参考：

- [`script.execScript()`](script.go:133)
- [`script.execPythonScript()`](script.go:155)
- [`script.execSysScript()`](script_windows.go:39)
- [`script.execSysScript()`](script_other.go:20)

### 2. 更可靠的退出码获取

直接依赖 [`ProcessState.ExitCode()`](https://pkg.go.dev/os#ProcessState.ExitCode) 在某些脚本场景下并不总是准确，尤其是包装脚本、多层脚本、异常退出、`set -e` 等情况。

为此，`xexec` 会在执行前注入环境变量：

- `XEXEC_TARGET`（目标脚本绝对路径）
- `XEXEC_EXIT_CODE_FILE`
- `XEXEC_SAVE_ENV_FILE`

包装脚本在退出时会主动把真实退出码写入文件，然后由 Go 侧通过 [`script.parseExitCodeFromFile()`](script.go:250) 读取，这使得退出码结果通常比单纯读取进程状态更可靠。

退出码文件是 `cmd.Wait()` 的补充，而不是替代：命令启动和 PID 等待权登记在同一把锁内完成，内部回收器只回收没有等待所有者的僵尸子进程，因此不会抢走脚本或命令的退出状态。宿主另行执行 `wait(-1)` 或将 `SIGCHLD` 设为 `SIG_IGN` 不在支持范围内；这类行为引发的 `ECHILD` 等等待错误会返回给调用方，不会因为存在退出码文件而被静默忽略。

相关流程可参考：

- [`ExecScript()`](script.go:72)
- [`script.mergeEnv()`](script.go:269)
- [`script.parseExitCodeFromFile()`](script.go:250)

### 3. 实时输出采集

[`script.exec()`](script.go:201) 会并发读取子进程的 stdout / stderr，并交由 [`script.consoleOutput()`](script_output.go:18) 按行处理。

输出处理具备以下特点：

- stdout / stderr 分通道并发消费，避免缓冲区阻塞
- 按行输出，实时性较好
- 对“没有换行符”的输出做 500ms 定时刷新
- 结束或取消时尽量刷出最后的残留内容
- 支持敏感词替换脱敏
- Windows 下支持 GBK 输出自动转 UTF-8

对应实现：

- [`script.exec()`](script.go:201)
- [`script.consoleOutput()`](script_output.go:18)
- [`script.transform()`](script_windows.go:135)
- [`script.gbkToUtf8()`](script_windows.go:146)

### 4. 超时取消与进程树清理

`xexec` 基于 [`context.Context`](https://pkg.go.dev/context) 工作，支持超时与主动取消。执行期间如果上下文取消：

- Windows 下通过原生 Toolhelp 进程快照和 `TerminateProcess` 尝试清理进程树；深度优先遍历、后代优先终止，每个分支处理完立即释放句柄，仅保留当前祖先链的句柄，使同时持有的句柄数受树深度而非进程总数限制；汇总清理错误。单次快照不提供 Job Object 那样的进程归属保证
- Unix/Linux 下通过进程组 + `SIGKILL` 清理子进程组（子进程以 `Setpgid` 成为新进程组组长，`Kill(-pid)` 即可终止整组）
- 统一将退出码文件写入 `137`，表示被强制终止

此外，在 Linux / FreeBSD 上脚本会设置 `Pdeathsig=SIGKILL`，由内核在父线程死亡时向直接子进程发送信号。它不是整个进程树的崩溃清理保证，需要注意以下边界：

- `Pdeathsig` 是「单进程级」信号，只能清理 Go 的**直接子进程**（包装 sh）；包装脚本或目标脚本内部 fork 出的孙进程（如脚本里的 `cmd &`）够不到，崩溃时仍可能残留
- 正常 cancel / 超时路径会通过 `Kill(-pid)` 终止同一进程组；主动通过 `setsid` 等方式脱离进程组的后代不在保证范围内
- macOS / OpenBSD / NetBSD 等无 `Pdeathsig` 字段的平台仅设 `Setpgid`，崩溃清理能力退化
- 若需要在崩溃场景下也可靠清理整棵进程树，应在部署层通过容器 / systemd cgroup 等边界兜底。孤儿回收只处理已经退出的子进程，不等同于终止仍存活的后代

对应实现：

- [`script.beforeExec()`](script_windows.go:100)
- [`script.beforeExec()`](script_other.go:53)
- [`sysProcAttr()`](script_pdeathsig.go:1)

### 5. 脚本回传环境变量

除了返回退出码外，[`ExecScript()`](script.go:72) 还可以返回 `map[string]any` 类型的环境变量结果。

脚本只需要把内容写入 `XEXEC_SAVE_ENV_FILE` 指向的文件中，Go 侧就会通过 [`script.parseSaveEnvFile()`](script.go:320) 解析。

支持两种格式：

#### 单行格式

```text
KEY=VALUE
```

#### 多行格式

```text
KEY<<EOF
line1
line2
EOF
```

这适合回传：

- token
- JSON
- 多行证书
- 大段文本

同时库内部默认禁止覆盖当前进程已经存在的系统环境变量，减少脚本误修改关键变量的风险。

### 6. 日志抽象与敏感信息脱敏

日志接口由 [`ILogger`](script_logger.go:13) 定义，调用方可以注入自己的日志实现，例如统一接入业务日志系统。

如果使用 [`WithScriptSecrets()`](script_options.go:65)，则标准输出和标准错误中出现的敏感字符串会在落日志前被替换成 `***`。

### 7. 模板变量注入

[`Inject()`](inject.go:39) 提供轻量级字符串变量占位符替换能力，可用于在执行脚本前动态拼装脚本内容、配置模板或命令文本。

支持如下占位形式：

- `%VAR%`
- `${VAR}`
- `$VAR`
- `{{VAR}}`
- `{VAR}`

变量查找优先级：

1. 调用时传入的 `envs map[string]any`
2. 系统环境变量

若变量不存在，则保留原始占位符。

实现位置：[`Inject()`](inject.go:39)

---

## 异步命令执行与孤儿回收

### 命令执行

`ExecContext` 启动命令后立即返回 `*XCmd`，调用方负责等待退出：

```go
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()

cmd, err := xexec.ExecContext(ctx, "/bin/sh",
    xexec.WithExecArgs("-c", "echo ready"),
    xexec.WithExecWorkingDir("/tmp"),
    xexec.WithExecEnv("MODE=production"),
)
if err != nil {
    return err
}
return cmd.Wait()
```

- 选项为 `WithExecArgs`、`WithExecWorkingDir`、`WithExecEnv`。
- 必须调用 **`XCmd.Wait()`**；不要绕过封装调用嵌入的 `Cmd.Wait()`。封装会在等待结束后释放 PID 等待权并关闭控制台输出文件。
- 命令控制台文件默认最多保留 50 MiB；命令及参数用于生成稳定的文件名指纹。
- Unix 命令使用独立进程组，Linux / FreeBSD 命令设置 `Pdeathsig=SIGTERM`；脚本继续使用 `Pdeathsig=SIGKILL`。Linux 的父死亡信号跟踪创建子进程的 OS 线程，且只作用于直接子进程。
- `KillAll(pid int32)` 在 Unix 上优先终止进程组，失败后尝试单独 PID；仅 `ESRCH` 视为幂等成功，其他错误会保留并返回。Unix 拒绝 `pid <= 1`。Windows 拒绝 `pid <= 0` 和 PID 4，使用原生进程快照及句柄进行尽力清理。非法 PID 在发送信号或调用 Windows 原生 API 前返回错误。传入 `os.Process.Pid` 时使用 `int32(process.Pid)`。

### Linux PID 1 自动回收

当宿主自身是 Linux PID 1 时，第一次启动命令或脚本会自动启用内部孤儿回收器。宿主仍然是 PID 1，不增加 init 父进程，也不需要显式调用启动函数。其他平台及普通非 PID 1 宿主不会自动启动回收循环。

- `childReaper` 统一持有命令等待所有者、启动锁和孤儿回收状态，不存在独立的 `waitRegistry`。
- 启动与 PID 登记和回收扫描共享一把锁，等待所有权一直保留到 `Wait()` 返回。
- 枚举 `/proc/self/task/*/children`，只对未被持有等待权的僵尸直接子进程按明确 PID 调用 `wait4`，不使用全局 `wait4(-1)`。
- `SIGCHLD` 通知与周期扫描共同驱动回收；脚本登记自己的真实 PID，不会在整个脚本执行期间暂停所有孤儿回收。
- 等待权协调、回收器启动和配置均为内部实现，不提供 `StartManaged`、`BeginOpaqueWait`、`MakeConfig` 或包级 `Start` 等公开接口。
- 调用方自行通过 `os/exec` 启动且尚未等待的子进程不在内部等待权登记范围内；PID 1 宿主应统一通过本库执行命令与脚本，避免第三方全局回收器竞争。

## 项目结构

```text
.
├── inject.go              # 变量注入能力
├── exec.go                # 异步命令与等待权释放
├── exec_console.go        # 有界控制台文件输出
├── exec_other.go          # Unix 命令初始化与取消
├── exec_windows.go        # Windows 命令初始化与取消
├── reaper.go              # 统一等待权与回收状态
├── reaper_linux.go        # Linux PID 1 孤儿回收
├── reaper_other.go        # 其他平台兼容实现
├── subreaper_linux.go     # Linux 子回收器设置
├── syscall_other.go       # Unix 进程组终止
├── syscall_windows.go     # Windows 进程树终止
├── script.go              # 核心执行流程
├── script_options.go      # 函数选项
├── script_output.go       # 输出流处理
├── script_logger.go       # 日志接口与默认实现
├── script_windows.go      # Windows 平台实现
├── script_other.go        # 非 Windows 平台实现
├── script_pdeathsig.go    # Linux/FreeBSD：Setpgid + Pdeathsig
├── script_nopdeathsig.go  # 其余 Unix（macOS 等）：仅 Setpgid
├── test/
│   ├── test.bat
│   ├── test.ps1
│   ├── test.py
│   └── test.sh
└── go.mod
```

---

## 安装

### 通过 `go get`

```bash
go get github.com/xmapst/xexec
```

### 依赖要求

当前模块信息见 [`go.mod`](go.mod)：

- Go：`1.26.3`
- 依赖：
  - `golang.org/x/sys`
  - `golang.org/x/text`

---

## 快速开始

### 执行现有脚本文件

```go
package main

import (
    "context"
    "fmt"
    "log"
    "time"

    "github.com/xmapst/xexec"
)

func main() {
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()

    code, env, err := xexec.ExecScript(
        ctx,
        "./test/test.sh",
        xexec.WithScriptArgs("hello", "world"),
        xexec.WithScriptEnv("DEMO=1"),
    )
    if err != nil {
        log.Println("exec error:", err)
    }

    fmt.Println("exit code:", code)
    fmt.Println("returned env:", env)
}
```

### 执行动态生成的脚本内容

使用 [`ExecScriptContent()`](script.go:49) 可以避免自己管理临时文件。

```go
package main

import (
    "context"
    "fmt"

    "github.com/xmapst/xexec"
)

func main() {
    content := `echo "hello from temp script"`

    code, env, err := xexec.ExecScriptContent(context.Background(), "sh", content)
    fmt.Println(code, env, err)
}
```

---

## API 说明

### [`ExecScript()`](script.go:72)

函数签名：

```go
func ExecScript(ctx context.Context, path string, options ...ScriptOption) (code int, env map[string]any, err error)
```

#### 参数

- `ctx`：用于控制超时、取消
- `path`：脚本路径
- `options`：脚本执行选项

#### 返回值

- `code`：脚本退出码
- `env`：脚本导出的环境变量
- `err`：执行错误；若上下文已取消，则优先返回上下文错误

### [`ExecScriptContent()`](script.go:49)

函数签名：

```go
func ExecScriptContent(ctx context.Context, kind, content string, options ...ScriptOption) (code int, env map[string]any, err error)
```

#### 说明

- `kind` 用于决定临时文件后缀，例如 `sh`、`py`、`ps1`
- `content` 为脚本内容
- 执行完毕后临时文件会自动删除

---

## 选项说明

所有选项都基于 [`ScriptOption`](script_options.go:10) 函数选项模式。

### [`WithScriptWorkdir()`](script_options.go:15)

设置脚本执行时的工作目录。

```go
xexec.WithScriptWorkdir("./test")
```

### [`WithScriptEnv()`](script_options.go:25)

为脚本设置附加环境变量，格式为 `KEY=VALUE`。

```go
xexec.WithScriptEnv(
    "APP_ENV=prod",
    "TOKEN=secret-token",
)
```

### [`WithScriptArgs()`](script_options.go:35)

向脚本传参。

```go
xexec.WithScriptArgs("arg1", "arg2")
```

### [`WithScriptStdin()`](script_options.go:44)

为脚本提供标准输入。

```go
xexec.WithScriptStdin(strings.NewReader("line1\nline2\n"))
```

### [`WithScriptLogger()`](script_options.go:54)

注入自定义日志实现，只需满足 [`ILogger`](script_logger.go:13) 接口。

```go
type myLogger struct{}

func (l *myLogger) Debugf(format string, args ...any) {}
func (l *myLogger) Infof(format string, args ...any)  {}
func (l *myLogger) Warnf(format string, args ...any)  {}
func (l *myLogger) Errorf(format string, args ...any) {}

xexec.WithScriptLogger(&myLogger{})
```

### [`WithScriptSecrets()`](script_options.go:65)

设置需要在日志中脱敏的敏感文本。

```go
xexec.WithScriptSecrets("password123", "secret-token")
```

---

## 环境变量回传约定

`xexec` 会在执行时自动注入以下内部环境变量：

- `XEXEC_TARGET`：目标脚本绝对路径（供包装脚本调用，避免路径插值）
- `XEXEC_EXIT_CODE_FILE`：退出码文件路径
- `XEXEC_SAVE_ENV_FILE`：环境变量回传文件路径

通常你不需要手动设置它们，但如果你希望让脚本向 Go 返回结构化结果，可以在脚本中写入 `XEXEC_SAVE_ENV_FILE`。

### Shell 示例

```sh
echo 'RESULT=ok' >> "$XEXEC_SAVE_ENV_FILE"
cat >> "$XEXEC_SAVE_ENV_FILE" <<'EOF'
DETAIL<<JSON
{"name":"demo","ok":true}
JSON
EOF
```

### PowerShell 示例

```powershell
Add-Content -Path $env:XEXEC_SAVE_ENV_FILE -Value 'RESULT=ok'
Add-Content -Path $env:XEXEC_SAVE_ENV_FILE -Value 'DETAIL<<EOF'
Add-Content -Path $env:XEXEC_SAVE_ENV_FILE -Value '{"name":"demo","ok":true}'
Add-Content -Path $env:XEXEC_SAVE_ENV_FILE -Value 'EOF'
```

### Python 示例

```python
import os

save_file = os.environ.get("XEXEC_SAVE_ENV_FILE")
with open(save_file, "a", encoding="utf-8") as f:
    f.write("RESULT=ok\n")
    f.write("MESSAGE<<EOF\n")
    f.write("hello\nworld\n")
    f.write("EOF\n")
```

---

## 变量注入示例

### 基本用法

```go
content := `
api = "${API_URL}"
name = "{{NAME}}"
win = "%USERNAME%"
`

result := xexec.Inject(content, map[string]any{
    "API_URL": "https://example.com",
    "NAME":    "demo",
})
```

### 支持的占位符

| 格式 | 示例 |
| --- | --- |
| `%VAR%` | `%TOKEN%` |
| `${VAR}` | `${TOKEN}` |
| `$VAR` | `$TOKEN` |
| `{{VAR}}` | `{{TOKEN}}` |
| `{VAR}` | `{TOKEN}` |

---

## 日志与编码处理

### 默认日志行为

若未显式设置日志器，则使用 [`defaultLogger`](script_logger.go:24) 基于标准库 `slog` 输出日志。

### Windows 中文乱码处理

在 Windows 平台，[`script.transform()`](script_windows.go:135) 会结合系统活动代码页以及 GBK 检测逻辑，必要时自动将输出从 GBK 转成 UTF-8。

这对中文 Windows 环境下执行：

- `cmd.exe`
- `powershell.exe`
- 某些本地命令行工具

尤其有帮助。

---

## 取消与退出码约定

### 正常执行

正常情况下，返回脚本真实退出码。

### 异常执行

常见约定如下：

- `255`：执行器层面错误、包装脚本异常、解析失败等
- `137`：上下文取消或被强制终止时写入的标准退出码

注意：最终 `err` 与 `code` 需要结合一起判断。

推荐判断方式：

```go
code, env, err := xexec.ExecScript(ctx, path)
if err != nil {
    // 先判断 context 取消 / 超时
}
_ = env
_ = code
```

---

## 错误处理

包内定义或包装的错误统一使用 `errors.go` 中的公开类型 `*xexec.Error`。调用方应通过 `errors.AsType` 获取错误类型和上下文字段，而不是匹配错误文本；对于 context、文件系统和系统调用错误，使用 `errors.Is` 检查底层原因。包装错误通过 `Unwrap()` 保留原始错误，清理过程中汇总的多条错误也可以通过标准库的错误链 API 检查。

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `Op` | `string` | 失败的操作或校验 |
| `PID` | `int64` | 相关进程 ID，也可保留非法负数 PID 的原始值 |
| `Path` | `string` | 相关文件路径 |
| `Value` | `string` | 相关输入值或补充上下文 |
| `Err` | `error` | 底层原因，可包含合并错误 |

不适用的上下文字段保留零值。直接返回的标准库错误仍保留原类型，**并非所有失败都能断言为 `*xexec.Error`**。

```go
if xe, ok := errors.AsType[*xexec.Error](err); ok {
    fmt.Printf("operation=%s pid=%d path=%q\n", xe.Op, xe.PID, xe.Path)
}
```

例如，等待命令退出时，可以同时识别超时和原始进程退出错误：

```go
if errors.Is(err, context.DeadlineExceeded) {
    // 执行超时；err 仍可能包含进程退出或清理错误。
}
if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
    code := exitErr.ExitCode()
    _ = code
}
```

`errors.AsType` 返回错误树中第一个匹配项；存在多个同类型错误时，如需检查全部项，应继续遍历 `Unwrap() []error` 或 `Unwrap() error`，不要只依赖第一个匹配结果。

## 自动化测试

在目标平台运行公共测试和平台专项测试，并输出语句覆盖率：

```sh
go test -race -count=1 -timeout=120s '-coverprofile=coverage.out' ./...
go tool cover '-func=coverage.out'
```

测试包含变量注入、文件替换、日志脱敏/定时刷新、console 容量与并发、命令参数、脚本退出码/环境回传、取消清理和等待权登记。Python 测试需要安装解释器；Windows 的 PowerShell 测试需要 `powershell.exe`，缺失时会明确跳过。Windows 专项测试必须在 Windows 实际运行，交叉编译不代表运行通过。

Windows 专项用例还覆盖 GBK 检测/转换、进程创建属性、BAT/CMD 与 PowerShell 的参数/标准输入/工作目录/环境回传、真实父子进程树终止，以及原生进程树清理失败时脚本取消的根进程回退，以及遍历环/重复 PID、错误汇总和创建时间校验。深度优先遍历另有宽树与深链模拟测试，验证句柄峰值受树深度限制、祖先句柄保留、分支及时释放和后序终止/关闭。进程树测试使用测试二进制作为辅助进程，不访问网络；通过保留进程句柄检查退出并执行兜底清理。

在 Windows 上可单独运行这些用例（完整测试仍应使用上面的全量命令）：

```powershell
go test -race -run '^TestWindows' -count=1 -timeout=120s '-coverprofile=coverage-windows.out' ./...
go tool cover '-func=coverage-windows.out'
```


### Docker 中作为 PID 1

Linux 环境有 Docker 和本地 `golang:latest` 镜像时运行：

```sh
sh test/test-docker-pid1.sh
```

脚本把开启竞态检测和覆盖率的测试二进制直接作为容器入口，不使用 `--init`，测试内强制断言 `os.Getpid()==1`。验证懒启动回收器、并发命令退出状态、孤儿收养/僵尸回收、保留存活孤儿、opaque 等待保护以及脚本超时后的后代回收。容器禁用网络，使用只读根文件系统和可执行的临时目录。

默认不自动拉取镜像；可用 `XEXEC_DOCKER_IMAGE` 指定已有的兼容 Go/Linux 运行镜像，用 `XEXEC_DOCKER_COVERPROFILE` 保存独立的 PID 1 覆盖率报告。该报告只统计 PID 1 集成测试，不应与全量平台覆盖率直接相加。

[GitHub Actions](.github/workflows/tests.yml) 分别在 Linux、Windows 和 Docker PID 1 环境运行测试并保存覆盖率报告。

## 测试脚本

仓库自带了几份示例脚本，可用于手动验证执行行为：

- [`test/test.sh`](test/test.sh)
- [`test/test.py`](test/test.py)
- [`test/test.ps1`](test/test.ps1)
- [`test/test.bat`](test/test.bat)

这些测试脚本覆盖了：

- 参数读取
- 标准输入读取
- 持续输出 / 长时间运行
- 跨平台脚本执行行为

---

## 适用场景

`xexec` 适合以下场景：

- 运维自动化平台执行用户脚本
- CI/CD 系统触发多语言脚本任务
- Agent / Runner 执行外部任务
- 需要可靠退出码和实时日志的后台任务系统
- 需要脚本回传结果到 Go 进程的场景

---

## 设计说明

### 为什么不是简单封装 `exec.Command`？

因为实际执行脚本时通常会遇到这些问题：

- 不同平台脚本调用方式不同
- 输出需要实时消费，避免阻塞
- 退出码并不总是可靠
- 取消任务时常常要清理子进程树
- 日志需要脱敏
- 脚本可能需要把结果结构化回传给 Go

`xexec` 正是在这些痛点上做了工程化增强。

### 为什么要用包装脚本？

包装脚本主要承担两件事：

1. 更稳定地收集退出码
2. 给脚本注入统一运行约定

这是整个库实现跨平台一致行为的重要基础。

---

## 注意事项

1. `xexec` 依赖目标机器存在相应解释器或执行环境，例如：
   - Python 脚本需要 `python3`、`python` 或 `py`（按此顺序探测）
   - PowerShell 脚本需要 `powershell.exe`
   - Shell 脚本需要 `sh`
2. 若脚本自身再派生复杂子进程：正常取消 / 超时时会通过进程组清理整棵树；但在 Go 进程异常崩溃时，`Pdeathsig` 只能清理直接子进程，孙进程可能残留（详见「超时取消与进程树清理」一节）。
3. 若脚本输出极大且持续时间极长，应结合业务日志策略与资源限制使用。
4. `WithScriptEnv()` 会覆盖同名系统环境变量，这是设计行为，需要调用方自行控制。
5. `parseSaveEnvFile` 禁止覆盖当前系统环境变量，因此该“环境回传机制”更适合返回新变量，而不是修改已有变量。

---

## 许可证

本项目采用 [`LICENSE`](LICENSE) 中声明的开源许可证。