// Package skills 实现 TraceMind 的三级技能加载机制。
//
// 技能是结构化的排查手册，按以下三级接入模型上下文：
//
//   - L1 索引（Index）：所有技能的名字、一句话描述与触发词，
//     常驻上下文，让模型知道存在哪些技能。
//   - L2 摘要（Match）：事故描述或证据命中触发词时，
//     注入对应技能的简短摘要，作为调查方向的参考。
//   - L3 全量（Get/SkillContent）：模型通过 load_skill 工具
//     主动加载完整排查步骤，受加载预算与 LRU 淘汰约束。
//
// 技能内容属于参考知识，不是当前事故的观测证据。
package skills

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// Skill 表示一个可加载的排查技能。
type Skill struct {
	Name             string   `json:"name"`
	Description      string   `json:"description"`
	Triggers         []string `json:"triggers,omitempty"`
	RecommendedTools []string `json:"recommended_tools,omitempty"`
	Source           string   `json:"source,omitempty"`
	Content          string   `json:"content,omitempty"`
}

// parseSkill 解析单个技能 Markdown 文件。
// 文件必须包含 front-matter：name、description、triggers；
// recommended_tools 可选。正文为 L3 全量内容。
func parseSkill(path, root, content string) (Skill, error) {
	skill := Skill{Source: relativePath(root, path)}

	front, body, err := splitFrontMatter(content)
	if err != nil {
		return skill, fmt.Errorf("parse skill %s: %w", path, err)
	}

	fields := parseFrontMatterFields(front)

	skill.Name = strings.TrimSpace(fields["name"])
	skill.Description = strings.TrimSpace(fields["description"])
	skill.Triggers = splitList(fields["triggers"])
	skill.RecommendedTools = splitList(fields["recommended_tools"])
	skill.Content = strings.TrimSpace(body)

	if skill.Name == "" {
		return skill, fmt.Errorf("skill %s: front-matter requires a non-empty name", path)
	}
	if skill.Description == "" {
		return skill, fmt.Errorf("skill %s: front-matter requires a non-empty description", path)
	}
	if len(skill.Triggers) == 0 {
		return skill, fmt.Errorf("skill %s: front-matter requires at least one trigger", path)
	}

	return skill, nil
}

// splitFrontMatter 提取文件最外层 --- 包裹的 front-matter 与正文。
// 允许文件开头存在空行或注释行。
func splitFrontMatter(content string) (front, body string, err error) {
	lines := strings.Split(content, "\n")

	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "---" {
			start = i
			break
		}
	}
	if start < 0 {
		return "", "", errors.New("missing front-matter delimiter")
	}

	end := -1
	for i := start + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return "", "", errors.New("unterminated front-matter")
	}

	front = strings.Join(lines[start+1:end], "\n")
	body = strings.Join(lines[end+1:], "\n")
	return front, body, nil
}

// parseFrontMatterFields 解析 key: value 形式的 front-matter 字段。
// 只在第一个冒号处切分，允许描述文本中包含冒号。
func parseFrontMatterFields(front string) map[string]string {
	fields := make(map[string]string)

	for _, line := range strings.Split(front, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		index := strings.Index(line, ":")
		if index <= 0 {
			continue
		}

		key := strings.TrimSpace(line[:index])
		value := strings.TrimSpace(line[index+1:])
		fields[key] = value
	}

	return fields
}

// splitList 解析逗号分隔的列表值，兼容 YAML 的 [a, b] 写法。
func splitList(value string) []string {
	value = strings.Trim(strings.TrimSpace(value), "[]")

	var items []string
	for _, part := range strings.Split(value, ",") {
		part = strings.Trim(strings.TrimSpace(part), `"'`)
		if part != "" {
			items = append(items, part)
		}
	}

	return items
}

func relativePath(root, path string) string {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(relative)
}
