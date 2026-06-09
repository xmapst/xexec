package xexec

import (
	"io"
)

// ScriptOption 脚本执行选项的函数类型，使用函数选项模式配置 script 实例。
//
// 函数选项模式的优势：可选参数清晰、易于扩展新选项，且调用方无需关心不需要的参数。
type ScriptOption func(*script)

// WithScriptWorkdir 设置脚本的工作目录（执行时的当前目录）。
//
// 影响脚本内所有相对路径的解析，若不设置则继承父进程的工作目录。
func WithScriptWorkdir(path string) ScriptOption {
	return func(s *script) {
		s.dir = path
	}
}

// WithScriptEnv 设置脚本专用的环境变量列表。
//
// 格式为 "KEY=VALUE" 字符串列表，会与系统环境变量合并，
// 同名键时自定义环境变量优先级更高（覆盖系统变量）。
func WithScriptEnv(env ...string) ScriptOption {
	return func(s *script) {
		s.env = env
	}
}

// WithScriptArgs 设置传递给脚本的命令行参数。
//
// 这些参数会附加在脚本路径之后传给解释器，
// 在脚本内部可通过 $1, $2... 或 sys.argv 等方式访问。
func WithScriptArgs(args ...string) ScriptOption {
	return func(s *script) {
		s.args = args
	}
}

// WithScriptStdin 设置脚本的标准输入来源。
//
// 适合需要向脚本传递交互式输入或管道数据的场景，如传递密码、配置文件内容等。
func WithScriptStdin(stdin io.Reader) ScriptOption {
	return func(s *script) {
		s.stdin = stdin
	}
}

// WithScriptLogger 设置自定义日志记录器，替换默认的 slog 实现。
//
// 通常传入与业务系统相同的日志实现（如 xlog），
// 使脚本执行日志与业务日志统一输出，便于集中收集和分析。
func WithScriptLogger(logger ILogger) ScriptOption {
	return func(s *script) {
		s.logger = logger
	}
}

// WithScriptSecrets 设置需要在日志输出中脱敏的敏感字符串列表。
//
// 所有非空的敏感词会在输出到日志之前被替换为 "***"，
// 防止密钥、密码等敏感信息出现在日志文件中造成信息泄漏。
// 空字符串会被自动过滤，避免误将所有输出替换为 "***"。
func WithScriptSecrets(secrets ...string) ScriptOption {
	return func(s *script) {
		// 过滤空字符串，空字符串作为替换模式会匹配所有位置
		var validSecrets []string
		for _, sec := range secrets {
			if len(sec) > 0 {
				validSecrets = append(validSecrets, sec)
			}
		}
		s.secrets = append(s.secrets, validSecrets...)
	}
}
