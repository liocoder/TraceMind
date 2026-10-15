package tool

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// SkillLibrary 是 load_skill 工具所需的技能库最小接口。
// skills.Library 满足该接口。
type SkillLibrary interface {
	SkillContent(name string) (string, bool)
	SkillNames() []string
}

// LoadSkillTool 按名称加载技能的完整内容（L3）。
// 技能属于参考知识，其执行结果不作为当前事故的 Evidence。
type LoadSkillTool struct {
	library SkillLibrary
}

func NewLoadSkillTool(library SkillLibrary) *LoadSkillTool {
	return &LoadSkillTool{library: library}
}

func (t *LoadSkillTool) Name() string {
	return "load_skill"
}

func (t *LoadSkillTool) Description() string {
	return "Load the full content of an investigation skill by name. " +
		"Use when the skill index or L2 summaries are not detailed " +
		"enough for the current failure mode."
}

func (t *LoadSkillTool) Execute(
	ctx context.Context,
	input map[string]any,
) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if t == nil || t.library == nil {
		return Result{}, fmt.Errorf("skill library is not configured")
	}

	name, ok := input["name"].(string)
	if !ok || strings.TrimSpace(name) == "" {
		return Result{}, fmt.Errorf("name must be a non-empty string")
	}
	name = strings.TrimSpace(name)

	content, found := t.library.SkillContent(name)
	if !found {
		names := t.library.SkillNames()
		sort.Strings(names)

		return Result{
			Content: fmt.Sprintf(
				"skill %q not found. Available skills: %s",
				name,
				strings.Join(names, ", "),
			),
			Metadata: map[string]string{
				"source":   "skills-library",
				"skill":    name,
				"evidence": "false",
			},
		}, nil
	}

	return Result{
		Content: content,
		Metadata: map[string]string{
			"source":   "skills-library",
			"skill":    name,
			"evidence": "false",
		},
	}, nil
}
