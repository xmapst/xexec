package xexec

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"strings"
)

const varNameExpr = `[A-Za-z_][A-Za-z0-9_-]*`

var (
	// variablePlaceholder 匹配多种变量占位符风格的正则表达式。
	//
	// 支持的格式：
	//   - %VAR%：自定义风格，捕获组 1
	//   - ${VAR}：Shell 花括号风格，捕获组 2
	//   - $VAR：Shell 简写风格，捕获组 3
	//   - {{VAR}} 或 {{ VAR }}：模板风格（允许内部空白），捕获组 4
	//   - {VAR} 或 { VAR }：模板风格（允许内部空白），捕获组 5
	//
	// 变量名规则：必须以字母或下划线开头（[A-Za-z_]），
	// 后续可包含字母、数字、下划线、连字符（[A-Za-z0-9_-]）。
	placeholderPatterns = []string{
		`%(` + varNameExpr + `)%`,
		`\$\{(` + varNameExpr + `)\}`,
		`\$(` + varNameExpr + `)`,
		`\{\{\s*(` + varNameExpr + `)\s*\}\}`,
		`\{\s*(` + varNameExpr + `)\s*\}`,
	}
	variablePlaceholder = regexp.MustCompile(strings.Join(placeholderPatterns, "|"))
)

// Inject 替换内容字符串中的变量占位符。
//
// 变量查找优先级：envs 参数 > 系统环境变量（os.LookupEnv）。
// 若变量在两处均未找到，则保留原始占位符字符串不做替换。
// 当 envs 为 nil 时自动初始化为空 map，安全处理 nil 输入。
func Inject(content string, envs map[string]any) string {
	if envs == nil {
		envs = make(map[string]any)
	}

	// lookup 封装统一的变量查找逻辑，先查 envs 再查系统环境变量
	lookup := func(name string) (string, bool) {
		if val, ok := envs[name]; ok {
			return fmt.Sprintf("%v", val), true
		}
		if val, exists := os.LookupEnv(name); exists {
			return val, true
		}
		return "", false
	}

	content = variablePlaceholder.ReplaceAllStringFunc(content, func(match string) string {
		submatches := variablePlaceholder.FindStringSubmatch(match)
		// 遍历捕获组，找到第一个非空组即为变量名（多种种格式只会有一个组匹配）
		// 捕获组索引：1=%VAR%, 2=${VAR}, 3=$VAR, 4={{VAR}}, 5={VAR}
		var name string
		var groupIdx int
		for i := 1; i < len(submatches); i++ {
			if submatches[i] != "" {
				name = submatches[i]
				groupIdx = i
				break
			}
		}

		if name == "" {
			return match
		}

		if val, found := lookup(name); found {
			return val
		}

		// 变量未找到：归一化 {{ VAR }} / { VAR } 的内部空白，其他格式保留原样
		switch groupIdx {
		case 4:
			return "{{" + name + "}}"
		case 5:
			return "{" + name + "}"
		default:
			return match
		}
	})

	return content
}

// InjectArgs 替换命令行参数中的变量占位符。
func InjectArgs(args []string, envs map[string]any) []string {
	for i, arg := range args {
		args[i] = Inject(arg, envs)
	}
	return args
}

// InjectFile injects environment variables into a single file at the specified path.
// It reads the file, replaces placeholders, and writes back to the file.
// Rollback support: write new files first, and overwrite files only if there are no exceptions.
// If the file does not exist, it returns nil.
func InjectFile(path string, envs map[string]any) (err error) {
	var info fs.FileInfo
	info, err = os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			// If the file does not exist, return nil
			return
		}
		return fmt.Errorf("failed to open file")
	}

	if info.Size() == 0 {
		// If the file is empty, return nil
		return
	}

	// Open the file for reading
	inFile, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("failed to open file")
	}
	defer func() {
		_ = inFile.Close()
	}()

	var tempPath = path + ".tmp"
	defer func() {
		_ = os.Remove(tempPath)
	}()

	// Create a temporary file for writing
	tmpFile, err := os.OpenFile(tempPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, info.Mode())
	if err != nil {
		return fmt.Errorf("failed to open file")
	}
	defer func() {
		_ = tmpFile.Close()
	}()

	// Read the file line by line
	const bufferSize = 64 * 1024
	buf := make([]byte, bufferSize)
	scanner := bufio.NewScanner(inFile)
	scanner.Buffer(buf, bufferSize)
	writer := bufio.NewWriter(tmpFile)
	hasChanges := false
	for scanner.Scan() {
		line := scanner.Text()
		// Replace placeholders in the line
		newLine := Inject(line, envs)
		if newLine != line {
			// Mark that changes were made
			hasChanges = true
		}
		// Write the modified line to the temporary file
		_, err = fmt.Fprintln(writer, newLine)
		if err != nil {
			return fmt.Errorf("failed to write to file")
		}
	}
	if err = scanner.Err(); err != nil {
		return fmt.Errorf("failed to write to file")
	}

	// Flush the writer to ensure all data is written
	if err = writer.Flush(); err != nil {
		return fmt.Errorf("failed to write to file")
	}
	if !hasChanges {
		// No changes made, do not overwrite the original file
		return
	}
	if err = os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("failed to write to file")
	}
	return
}
