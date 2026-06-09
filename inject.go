package xexec

import (
	"fmt"
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
