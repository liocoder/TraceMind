package skills

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Library 持有全部技能，并提供三级访问能力：
//
//   - L1 索引：Index() 生成常驻上下文的紧凑清单。
//   - L2 摘要：Match() 按触发词命中返回相关技能摘要。
//   - L3 全量：Get()/SkillContent() 返回完整排查步骤。
type Library struct {
	skills []Skill
	byName map[string]Skill
}

// Load 加载 root 目录下的所有技能 Markdown 文件。
// 技能在初始化时一次性读取，后续访问不重复读盘。
func Load(root string) (*Library, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, errors.New("skills root directory cannot be empty")
	}

	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("stat skills directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("skills path is not a directory: %s", root)
	}

	lib := &Library{byName: make(map[string]Skill)}

	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(path), ".md") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read skill %s: %w", path, err)
		}

		skill, err := parseSkill(path, root, string(data))
		if err != nil {
			return err
		}

		if _, exists := lib.byName[skill.Name]; exists {
			return fmt.Errorf("duplicate skill name %q in %s", skill.Name, path)
		}

		lib.byName[skill.Name] = skill
		lib.skills = append(lib.skills, skill)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("load skills: %w", err)
	}

	if len(lib.skills) == 0 {
		return nil, fmt.Errorf("no skills found in %s", root)
	}

	sort.Slice(lib.skills, func(i, j int) bool {
		return lib.skills[i].Name < lib.skills[j].Name
	})

	return lib, nil
}

// Skills 返回按名称排序的全部技能。
func (l *Library) Skills() []Skill {
	if l == nil {
		return nil
	}
	return append([]Skill(nil), l.skills...)
}

// Get 按名称返回技能。
func (l *Library) Get(name string) (Skill, bool) {
	if l == nil {
		return Skill{}, false
	}
	skill, ok := l.byName[name]
	return skill, ok
}

// SkillContent 返回技能全量正文（L3），供 load_skill 工具使用。
func (l *Library) SkillContent(name string) (string, bool) {
	skill, ok := l.Get(name)
	if !ok {
		return "", false
	}
	return skill.Content, true
}

// SkillNames 返回全部技能名称。
func (l *Library) SkillNames() []string {
	if l == nil {
		return nil
	}
	names := make([]string, 0, len(l.skills))
	for _, skill := range l.skills {
		names = append(names, skill.Name)
	}
	return names
}

// Index 生成 L1 索引：所有技能的名字、描述与触发词，常驻上下文。
func (l *Library) Index() string {
	if l == nil {
		return ""
	}

	var builder strings.Builder
	builder.WriteString("Available investigation skills (L1 index):\n")

	for _, skill := range l.skills {
		builder.WriteString("- " + skill.Name + ": " + skill.Description)
		if len(skill.Triggers) > 0 {
			builder.WriteString(" (triggers: " + strings.Join(skill.Triggers, ", ") + ")")
		}
		builder.WriteString("\n")
	}

	builder.WriteString(
		"When an L2 summary is not detailed enough, call load_skill to load " +
			"the full skill content.\n",
	)

	return builder.String()
}

// Match 返回触发词命中查询的技能（L2 摘要候选）。
// 匹配为大小写不敏感的包含匹配，支持英文子串与中文子串。
func (l *Library) Match(query string) []Skill {
	if l == nil {
		return nil
	}

	query = normalizeText(query)
	if query == "" {
		return nil
	}

	var matched []Skill
	for _, skill := range l.skills {
		if skillMatches(skill, query) {
			matched = append(matched, skill)
		}
	}

	return matched
}

func skillMatches(skill Skill, query string) bool {
	for _, trigger := range skill.Triggers {
		trigger = normalizeText(trigger)
		if trigger != "" && strings.Contains(query, trigger) {
			return true
		}
	}
	return false
}

func normalizeText(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
