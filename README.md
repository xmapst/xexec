# xexec

`xexec` 是一个对 Go 标准库 [`os/exec`](https://pkg.go.dev/os/exec) 的增强封装，面向“执行脚本”这一类高频场景，提供比直接调用 [`exec.Command`](https://pkg.go.dev/os/exec#Command) 更稳定、可观测、跨平台的能力。

该项目重点解决以下问题：

- 跨平台执行不同类型脚本：Shell、Batch、PowerShell、Python
- 获取更可靠的真实退出码，而不是仅依赖进程状态
- 实时读取并输出标准输出 / 标准错误
- 支持超时、取消、整棵进程树清理
- 支持脚本向 Go 侧“回传环境变量”
- 支持日志脱敏与自定义日志实现
- 支持模板变量注入
- 兼顾 Windows 中文编码场景，尽量避免乱码

## 功能特性

### 1. 多脚本类型执行

核心入口 [`ExecScript()`](script.go:72) 会根据脚本扩展名自动选择执行方式：

- Python 脚本：`.py`、`.py3`
- Windows 脚本：`.ps1`、`.ps`、`.bat` 等
- Unix/Linux Shell 脚本：如 `.sh`

其中：

- Python 使用包装器脚本配合 `runpy.run_path()` 执行，保证 `if __name__ == '__main__'` 语义正确
- Windows 下自动区分 `powershell.exe` 与 `cmd.exe`
- 非 Windows 平台统一走 `sh` 包装执行

实现位置可参考：

- [`script.execScript()`](script.go:133)
- [`script.execPythonScript()`](script.go:155)
- [`script.execSysScript()`](script_windows.go:39)
- [`script.execSysScript()`](script_other.go:20)

### 2. 更可靠的退出码获取

直接依赖 [`ProcessState.ExitCode()`](https://pkg.go.dev/os#ProcessState.ExitCode) 在某些脚本场景下并不总是准确，尤其是包装脚本、多层脚本、异常退出、`set -e` 等情况。

为此，`xexec` 会在执行前注入环境变量：

- `XEXEC_EXIT_CODE_FILE`
- `XEXEC_SAVE_ENV_FILE`

包装脚本在退出时会主动把真实退出码写入文件，然后由 Go 侧通过 [`script.parseExitCodeFromFile()`](script.go:250) 读取，这使得退出码结果通常比单纯读取进程状态更可靠。

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

- Windows 下通过 `TASKKILL /T /F` 尝试清理整棵进程树
- Unix/Linux 下通过进程组 + `SIGKILL` 清理子进程组
- 统一将退出码文件写入 `137`，表示被强制终止

对应实现：

- [`script.beforeExec()`](script_windows.go:100)
- [`script.beforeExec()`](script_other.go:53)

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

## 项目结构

```text
.
├── inject.go              # 变量注入能力
├── script.go              # 核心执行流程
├── script_options.go      # 函数选项
├── script_output.go       # 输出流处理
├── script_logger.go       # 日志接口与默认实现
├── script_windows.go      # Windows 平台实现
├── script_other.go        # 非 Windows 平台实现
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

`xexec` 会在执行时自动注入两个内部环境变量：

- `XEXEC_EXIT_CODE_FILE`
- `XEXEC_SAVE_ENV_FILE`

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
   - Python 脚本需要 `python`
   - PowerShell 脚本需要 `powershell.exe`
   - Shell 脚本需要 `sh`
2. 若脚本自身再派生复杂子进程，虽然库已尽量清理进程树，但不同平台的行为仍可能存在差异。
3. 若脚本输出极大且持续时间极长，应结合业务日志策略与资源限制使用。
4. `WithScriptEnv()` 会覆盖同名系统环境变量，这是设计行为，需要调用方自行控制。
5. `parseSaveEnvFile` 禁止覆盖当前系统环境变量，因此该“环境回传机制”更适合返回新变量，而不是修改已有变量。

---

## 许可证

本项目采用 [`LICENSE`](LICENSE) 中声明的开源许可证。