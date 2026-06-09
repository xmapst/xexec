package xexec

import (
	"fmt"
	"log/slog"
)

// ILogger 脚本执行器的日志接口，支持自定义日志后端。
//
// 通过接口抽象解耦日志实现与脚本执行逻辑，
// 调用方可通过 WithScriptLogger 选项注入自定义实现（如 xlog、zap 等），
// 默认使用标准库 slog 输出到 stderr。
type ILogger interface {
	Debugf(format string, args ...any)
	Infof(format string, args ...any)
	Warnf(format string, args ...any)
	Errorf(format string, args ...any)
}

// defaultLogger 基于标准库 slog 的默认日志实现。
//
// 当 args 为空时直接使用 format 字符串作为消息，
// 避免 fmt.Sprintf 在无参数时产生多余的格式化开销。
type defaultLogger struct{}

func (l *defaultLogger) Debugf(format string, args ...any) {
	if len(args) == 0 {
		slog.Debug(format)
	} else {
		slog.Debug(fmt.Sprintf(format, args...))
	}
}

func (l *defaultLogger) Infof(format string, args ...any) {
	if len(args) == 0 {
		slog.Info(format)
	} else {
		slog.Info(fmt.Sprintf(format, args...))
	}
}

func (l *defaultLogger) Warnf(format string, args ...any) {
	if len(args) == 0 {
		slog.Warn(format)
	} else {
		slog.Warn(fmt.Sprintf(format, args...))
	}
}

func (l *defaultLogger) Errorf(format string, args ...any) {
	if len(args) == 0 {
		slog.Error(format)
	} else {
		slog.Error(fmt.Sprintf(format, args...))
	}
}
