package agent

import (
	"regexp"
	"strings"
)

// jsonValueCaseRe 匹配冒号后的大写布尔/空值写法。
// 只匹配值位置（冒号后紧接词），避免误改字符串内部内容。
var jsonValueCaseRe = regexp.MustCompile(`:\s*(True|False|None|Null|NULL)\b`)

// jsonTrailingCommaRe 匹配数组/对象末尾的尾逗号。
var jsonTrailingCommaRe = regexp.MustCompile(`,\s*([}\]])`)

// repairJSON 修复 LLM 输出中常见的非严格 JSON 写法：
//
//   - 大写 True / False / None / Null / NULL（Go 的 json 不接受大写布尔）
//   - 数组/对象末尾的尾逗号
//
// 修复以值位置为锚点，尽可能避免改动字符串内容。
// 没有任何可修复项时返回原样，由调用方决定是否报错。
func repairJSON(content string) string {
	content = jsonValueCaseRe.ReplaceAllStringFunc(content, func(match string) string {
		idx := strings.LastIndex(match, ":")
		if idx < 0 {
			return match
		}

		var normalized string
		switch strings.TrimSpace(match[idx+1:]) {
		case "True":
			normalized = "true"
		case "False":
			normalized = "false"
		case "None", "Null", "NULL":
			normalized = "null"
		default:
			return match
		}

		return match[:idx+1] + " " + normalized
	})

	// 尾逗号可能嵌套出现，做有上限的迭代修复。
	for i := 0; i < 3; i++ {
		repaired := jsonTrailingCommaRe.ReplaceAllString(content, "$1")
		if repaired == content {
			break
		}
		content = repaired
	}

	return content
}
