package xexec

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	// digitRe 匹配连续数字序列，用于从退出码文件中提取数字。
	digitRe = regexp.MustCompile(`\d+`)
)

// script 脚本执行器的核心结构体，封装单次脚本执行的所有上下文。
//
// 通过函数选项模式（ScriptOption）配置执行参数，
// 支持 Python、Shell、PowerShell、Batch 等多种脚本类型。
type script struct {
	cmd          *exec.Cmd          // 底层系统命令对象
	ctx          context.Context    // 执行上下文，用于超时控制和取消
	cancel       context.CancelFunc // 取消函数，脚本完成或超时后调用
	env          []string           // 自定义环境变量列表（格式 KEY=VALUE）
	args         []string           // 传递给脚本的命令行参数
	dir          string             // 脚本工作目录
	path         string             // 脚本文件绝对路径
	stdin        io.Reader          // 标准输入，nil 表示不提供输入
	exitCodePath string             // 退出码文件路径（通过 XEXEC_EXIT_CODE_FILE 环境变量传递给包装脚本）
	saveEnvPath  string             // 环境变量导出文件路径（脚本可写入供 Go 读取）
	logger       ILogger            // 日志记录器
	secrets      []string           // 需要脱敏的敏感字符串列表
	masker       *strings.Replacer  // 基于 secrets 构建的字符串替换器，执行日志脱敏
}

// ExecScriptContent 将脚本内容写入临时文件后执行，执行完毕自动清理。
//
// 适合动态生成脚本内容的场景，避免调用方手动管理临时文件。
// 临时文件名包含纳秒时间戳，保证并发执行时文件名不冲突。
func ExecScriptContent(ctx context.Context, kind, content string, options ...ScriptOption) (code int, env map[string]any, err error) {
	scriptPath := filepath.Join(os.TempDir(), fmt.Sprintf(".script_%d.%s", time.Now().UnixNano(), kind))
	defer func() {
		_ = os.Remove(scriptPath)
	}()
	err = os.WriteFile(scriptPath, []byte(content), os.ModePerm)
	if err != nil {
		return 255, nil, err
	}
	return ExecScript(ctx, scriptPath, options...)
}

// ExecScript 执行指定路径的脚本文件，返回退出码、导出的环境变量和错误信息。
//
// 执行流程：
//  1. 构建 script 实例并应用所有选项
//  2. 构建脱敏替换器
//  3. 根据文件扩展名选择解释器（.py → Python，其他 → 系统 Shell）
//  4. 从退出码文件读取真实退出码（包装脚本捕获的值比 ProcessState 更准确）
//  5. 解析脚本导出的环境变量
//
// 退出码设计：包装脚本通过 XEXEC_EXIT_CODE_FILE 环境变量指定的文件写入退出码，
// 比直接读取 cmd.ProcessState.ExitCode() 更可靠（能正确处理 set -e 等场景）。
func ExecScript(ctx context.Context, path string, options ...ScriptOption) (code int, env map[string]any, err error) {
	if ctx == nil {
		ctx = context.Background()
	}

	s := &script{
		path:   path,
		logger: &defaultLogger{},
	}
	s.exitCodePath = filepath.Join(os.TempDir(), s.randomFilename("exitCode", ".txt"))
	s.saveEnvPath = filepath.Join(os.TempDir(), s.randomFilename("saveEnv", ".txt"))
	s.abs()
	_ = os.Chmod(path, os.ModePerm) // 确保脚本文件有执行权限
	_, err = os.Stat(path)
	if err != nil {
		return 255, nil, fmt.Errorf("stat script: %w", err)
	}
	s.ctx, s.cancel = context.WithCancel(ctx)
	defer func() {
		s.cancel()
		_ = os.Remove(s.exitCodePath)
		_ = os.Remove(s.saveEnvPath)
	}()
	for _, option := range options {
		option(s)
	}

	// 构建脱敏替换器：将 secrets 列表转换为 [secret, "***", ...] 的替换对
	replacements := make([]string, 0, len(s.secrets)*2)
	for _, secret := range s.secrets {
		replacements = append(replacements, secret, "***")
	}
	if len(replacements) > 0 {
		s.masker = strings.NewReplacer(replacements...)
	}

	defer func() {
		// 若上下文已取消，优先返回 context 错误（超时/取消比脚本错误更重要）
		if s.ctx.Err() != nil {
			err = s.ctx.Err()
		}
	}()
	code, err = s.execScript()
	// 优先使用包装脚本写入的退出码文件（更准确），失败则保留 ProcessState 的值
	code1, err1 := s.parseExitCodeFromFile()
	if err1 == nil {
		code = code1
	}
	env = s.parseSaveEnvFile()
	return code, env, err
}

// abs 将脚本路径转换为绝对路径。
func (s *script) abs() {
	if filepath.IsAbs(s.path) {
		s.path = filepath.Clean(s.path)
	}
	s.path = filepath.Join(s.dir, s.path)
}

// execScript 根据文件扩展名分发到对应的执行逻辑。
func (s *script) execScript() (code int, err error) {
	ext := filepath.Ext(s.path)
	switch ext {
	case ".py", ".py3":
		return s.execPythonScript()
	default:
		return s.execSysScript()
	}
}

// randomFilename 生成包含纳秒时间戳的唯一临时文件名，防止并发执行时文件名冲突。
func (s *script) randomFilename(prefix, ext string) string {
	return fmt.Sprintf("%s-%d%s", prefix, time.Now().UnixNano(), ext)
}

// execPythonScript 通过 Python 包装脚本执行目标 Python 文件。
//
// 使用 runpy.run_path 而非直接执行，原因：
//   - 正确设置 __main__ 上下文，使 if __name__ == '__main__' 生效
//   - 正确透传 sys.argv（脚本路径 + 用户参数）
//   - 捕获 SystemExit 异常，将退出码写入文件，保证退出码的准确性
//   - -u 参数禁用 Python 输出缓冲，确保实时看到脚本输出
func (s *script) execPythonScript() (code int, err error) {
	wrapperContent := fmt.Sprintf(`# -*- coding: utf-8 -*-
import sys
import os
import runpy

exit_code = 0
script_path = r'%s'

# 设置 sys.argv，第一个参数是脚本路径，后续是传入的参数
sys.argv = [script_path] + sys.argv[1:]

try:
    runpy.run_path(script_path, run_name='__main__')
except SystemExit as e:
    exit_code = e.code if e.code is not None else 0
except Exception:
    import traceback
    traceback.print_exc()
    exit_code = 255
finally:
    code_file = os.environ.get('XEXEC_EXIT_CODE_FILE')
    if code_file and not os.path.exists(code_file):
        try:
            with open(code_file, 'w') as f:
                f.write(str(exit_code if isinstance(exit_code, int) else 1))
        except:
            pass
    sys.exit(exit_code if isinstance(exit_code, int) else 1)
`, s.path)
	wrapperPath := filepath.Join(os.TempDir(), s.randomFilename("wrapper", ".py"))
	defer func() {
		_ = os.Remove(wrapperPath)
	}()
	if err = os.WriteFile(wrapperPath, []byte(wrapperContent), os.ModePerm); err != nil {
		return 255, err
	}
	args := append([]string{"-u", wrapperPath}, s.args...)
	return s.exec("python", args...)
}

// exec 执行指定命令，并发读取 stdout/stderr 并实时输出日志。
//
// 使用两个 goroutine 分别读取 stdout 和 stderr，通过 WaitGroup 等待两者完成后再返回，
// 防止在 cmd.Wait() 之前缓冲区溢出导致进程阻塞。
// "no child processes" 错误表示进程已被其他线程回收（僵尸进程），静默忽略此错误。
func (s *script) exec(command string, args ...string) (code int, err error) {
	s.cmd = exec.CommandContext(s.ctx, command, args...)
	s.cmd.Env = s.mergeEnv()
	if s.dir != "" {
		s.cmd.Dir = s.dir
	}
	s.beforeExec() // 平台特定的进程属性设置（进程组、隐藏窗口等）
	if s.stdin != nil {
		s.cmd.Stdin = s.stdin
	}
	stdout, err := s.cmd.StdoutPipe()
	if err != nil {
		return 255, err
	}
	stderr, err := s.cmd.StderrPipe()
	if err != nil {
		return 255, err
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		s.consoleOutput("STDOUT", stdout)
	}()
	go func() {
		defer wg.Done()
		s.consoleOutput("STDERR", stderr)
	}()
	err = s.cmd.Run()
	wg.Wait() // 等待输出读取完毕，防止日志截断

	// 僵尸进程被操作系统提前回收时会产生此错误，属于正常情况，忽略
	if err != nil && strings.Contains(err.Error(), "waitid: no child processes") {
		err = nil
	}

	if err != nil {
		code = 255
	}
	if s.cmd.ProcessState != nil {
		code = s.cmd.ProcessState.ExitCode()
	}
	return code, err
}

// parseExitCodeFromFile 从退出码文件中读取并解析退出码数字。
//
// 包装脚本在退出前将真实退出码写入文件，
// 比读取 ProcessState.ExitCode() 更准确，能正确反映脚本内部 exit 调用的值。
func (s *script) parseExitCodeFromFile() (int, error) {
	data, err := os.ReadFile(s.exitCodePath)
	if err != nil {
		return 255, fmt.Errorf("read code file error: %w", err)
	}

	matches := digitRe.FindString(string(bytes.TrimSpace(data)))
	if matches == "" {
		return 255, fmt.Errorf("no valid exit code found")
	}

	return strconv.Atoi(matches)
}

// mergeEnv 合并系统环境变量和自定义环境变量，自定义环境变量优先级更高。
//
// 合并策略：先用 map 收集系统环境变量，再用自定义变量覆盖同名键，
// 最后追加 XEXEC_EXIT_CODE_FILE 和 XEXEC_SAVE_ENV_FILE 两个内置变量，
// 供包装脚本写入退出码和导出环境变量。
func (s *script) mergeEnv() []string {
	envMap := make(map[string]string)

	// 先加载系统环境变量作为基础
	for _, env := range syscall.Environ() {
		key := s.getEnvKey(env)
		if key != "" {
			envMap[key] = env
		}
	}

	// 自定义环境变量覆盖同名系统变量（优先级更高）
	for _, env := range s.env {
		key := s.getEnvKey(env)
		if key != "" {
			envMap[key] = env
		}
	}

	result := make([]string, 0, len(envMap))
	for _, env := range envMap {
		result = append(result, env)
	}

	// 注入内置变量：脚本通过这两个变量找到文件路径并写入数据
	result = append(
		result,
		fmt.Sprintf("XEXEC_EXIT_CODE_FILE=%s", s.exitCodePath),
		fmt.Sprintf("XEXEC_SAVE_ENV_FILE=%s", s.saveEnvPath),
	)

	return result
}

// getEnvKey 从 "KEY=VALUE" 格式的环境变量字符串中提取键名。
func (s *script) getEnvKey(env string) string {
	key, _, found := strings.Cut(env, "=")
	if !found {
		return ""
	}
	return key
}

// parseSaveEnvFile 解析脚本通过 XEXEC_SAVE_ENV_FILE 导出的环境变量文件。
//
// 文件格式支持两种语法：
//   - 单行：KEY=VALUE
//   - 多行：KEY<<DELIMITER\n...\nDELIMITER（类似 Bash here-document）
//
// 安全机制：禁止脚本覆盖系统已有的环境变量（通过 os.LookupEnv 检查），
// 防止脚本通过此机制篡改进程级别的环境变量（如 PATH、HOME 等）。
func (s *script) parseSaveEnvFile() map[string]any {
	var saveEnv = make(map[string]any)
	file, err := os.Open(s.saveEnvPath)
	if err != nil {
		return saveEnv
	}
	defer func() {
		_ = file.Close()
	}()

	scanner := bufio.NewScanner(file)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 10*1024*1024) // 单行最大 10MB，支持大段 JSON 值

	firstLine := true
	for scanner.Scan() {
		line := scanner.Text()

		// 处理 UTF-8 BOM（Windows 平台上某些编辑器会在文件开头插入 BOM）
		if firstLine {
			firstLine = false
			if len(line) >= 3 && line[0] == 0xEF && line[1] == 0xBB && line[2] == 0xBF {
				s.logger.Debugf("[SYSTEM] UTF-8 BOM detected and removed")
				line = line[3:]
			}
		}

		if strings.TrimSpace(line) == "" {
			continue
		}

		// 解析逻辑：通过 << 和 = 的相对位置判断是多行还是单行格式
		// 情况 A: 只有 =（没有 <<）→ 单行
		// 情况 B: 有 = 且 = 在 << 前 → 单行（值中包含 << 视为普通字符）
		// 情况 C: 有 << 且 << 在 = 前（或没有 =）→ 多行 here-document
		multiKey, multiRest, hasMulti := strings.Cut(line, "<<")
		singleKey, singleValue, hasSingle := strings.Cut(line, "=")

		isMultiLine := hasMulti && (!hasSingle || len(multiKey) < len(singleKey))

		if isMultiLine {
			key := strings.TrimSpace(multiKey)
			delimiter := strings.TrimSpace(multiRest)

			if key == "" || delimiter == "" {
				s.logger.Errorf("[SYSTEM] invalid multiline format: key or delimiter is empty in line '%s'", line)
				continue
			}

			var valueBuilder strings.Builder
			delimiterFound := false

			for scanner.Scan() {
				content := scanner.Text()
				if strings.TrimSpace(content) == delimiter {
					delimiterFound = true
					break
				}
				if valueBuilder.Len() > 0 {
					valueBuilder.WriteString("\n")
				}
				valueBuilder.WriteString(content)
			}

			if !delimiterFound {
				s.logger.Errorf("[SYSTEM] EOF reached before finding delimiter '%s'", delimiter)
				return saveEnv
			}

			// 禁止覆盖系统环境变量，防止脚本恶意修改进程环境
			if _, exists := os.LookupEnv(key); exists {
				s.logger.Warnf("[SYSTEM] ignored attempt to overwrite system variable: %s", key)
				continue
			}
			saveEnv[key] = valueBuilder.String()
			s.logger.Debugf("[SYSTEM] parsed env: %s", key)

		} else if hasSingle {
			key := strings.TrimSpace(singleKey)

			if key == "" {
				s.logger.Errorf("[SYSTEM] invalid format: empty key in line '%s'", line)
				continue
			}

			if _, exists := os.LookupEnv(key); exists {
				s.logger.Warnf("[SYSTEM] ignored attempt to overwrite system variable: %s", key)
				continue
			}
			saveEnv[key] = singleValue
			s.logger.Debugf("[SYSTEM] parsed env: %s", key)

		} else {
			s.logger.Errorf("[SYSTEM] invalid format '%v', expected 'KEY=VALUE' or 'KEY<<EOF'", line)
			continue
		}
	}

	if err = scanner.Err(); err != nil {
		s.logger.Errorf("[SYSTEM] error reading file: %v", err)
		return saveEnv
	}

	s.logger.Infof("[SYSTEM] successfully parsed %d environment variables.", len(saveEnv))
	return saveEnv
}
