package agent

import (
	"encoding/json"
	"strings"

	"github.com/liocoder/TraceMind/internal/llm"
	"github.com/liocoder/TraceMind/internal/skills"
)

// maxLoadedSkills 是单次事件调查中允许同时加载的技能数（L3 全量预算）。
const maxLoadedSkills = 2

// skillContextMarker 标记"当前技能上下文"这条 system 消息。
// 每轮调查后整条替换，保证 LRU 淘汰、模型可见上下文与报告记录三者一致：
// 被淘汰技能的旧正文不会残留在对话历史中重复占用 token。
const skillContextMarker = "[TraceMind skill context]"

// upsertSkillContextMessage 用最新的技能上下文替换 messages 中旧的技能上下文消息。
// 内容为空时不作任何修改。
func upsertSkillContextMessage(
	messages []llm.Message,
	content string,
) []llm.Message {
	if content == "" {
		return messages
	}

	marked := skillContextMarker + "\n" + content

	filtered := make([]llm.Message, 0, len(messages)+1)
	for _, message := range messages {
		if strings.HasPrefix(message.Content, skillContextMarker) {
			continue
		}
		filtered = append(filtered, message)
	}

	return append(filtered, llm.Message{
		Role:    "system",
		Content: marked,
	})
}

// SkillReference 记录调查过程中实际使用的技能来源，供报告审计。
// 技能属于参考知识，不属于 Evidence，不参与当前事故证据计数。
type SkillReference struct {
	Name  string `json:"name"`
	Level string `json:"level"` // summary | full
}

// SkillBrief 是传给规划器的技能知识摘要。
type SkillBrief struct {
	Name             string   `json:"name"`
	Level            string   `json:"level"` // summary | full
	Description      string   `json:"description,omitempty"`
	Content          string   `json:"content,omitempty"`
	RecommendedTools []string `json:"recommended_tools,omitempty"`
}

// skillLoader 维护本次事件调查中已加载的技能，带容量上限与 LRU 淘汰。
type skillLoader struct {
	max    int
	order  []string
	loaded map[string]struct{}
}

func newSkillLoader(max int) *skillLoader {
	if max <= 0 {
		max = maxLoadedSkills
	}
	return &skillLoader{
		max:    max,
		loaded: make(map[string]struct{}),
	}
}

// Load 记录技能已加载；容量满时淘汰最早加载的技能，返回被淘汰技能名。
// 已加载技能再次 Load 只刷新其最近使用顺序，不返回淘汰项。
func (l *skillLoader) Load(name string) string {
	if name == "" || l == nil {
		return ""
	}

	if _, ok := l.loaded[name]; ok {
		l.touch(name)
		return ""
	}

	evicted := ""
	if len(l.order) >= l.max {
		evicted = l.order[0]
		delete(l.loaded, evicted)
		l.order = l.order[1:]
	}

	l.loaded[name] = struct{}{}
	l.order = append(l.order, name)
	return evicted
}

func (l *skillLoader) touch(name string) {
	for i, loaded := range l.order {
		if loaded == name {
			l.order = append(l.order[:i], l.order[i+1:]...)
			l.order = append(l.order, name)
			return
		}
	}
}

// Loaded 返回已加载技能名（按加载顺序）。
func (l *skillLoader) Loaded() []string {
	if l == nil {
		return nil
	}
	return append([]string(nil), l.order...)
}

// buildSkillContext 组合 L2 摘要与 L3 全量，生成本轮技能上下文。
// 返回注入消息文本、报告审计引用和传给规划器的技能摘要。
// 没有任何匹配或加载时返回空串，调用方不应注入空消息。
func buildSkillContext(
	lib *skills.Library,
	query string,
	loader *skillLoader,
) (string, []SkillReference, []SkillBrief) {
	if lib == nil {
		return "", nil, nil
	}

	var refs []SkillReference
	var briefs []SkillBrief
	var builder strings.Builder

	matched := lib.Match(query)
	if len(matched) > 3 {
		matched = matched[:3]
	}

	if len(matched) > 0 {
		builder.WriteString("Matching investigation skills (L2 summaries):\n")
		for _, skill := range matched {
			refs = append(refs, SkillReference{
				Name:  skill.Name,
				Level: "summary",
			})
			briefs = append(briefs, SkillBrief{
				Name:             skill.Name,
				Level:            "summary",
				Description:      skill.Description,
				RecommendedTools: skill.RecommendedTools,
			})

			builder.WriteString("- " + skill.Name + ": " + skill.Description)
			if len(skill.RecommendedTools) > 0 {
				builder.WriteString(
					" (recommended tools: " + strings.Join(skill.RecommendedTools, ", ") + ")",
				)
			}
			builder.WriteString("\n")
		}
	}

	var loaded []skills.Skill
	if loader != nil {
		for _, name := range loader.Loaded() {
			if skill, ok := lib.Get(name); ok {
				loaded = append(loaded, skill)
			}
		}
	}

	if len(loaded) > 0 {
		builder.WriteString("\nLoaded skills (L3 full content):\n")
		for _, skill := range loaded {
			refs = append(refs, SkillReference{
				Name:  skill.Name,
				Level: "full",
			})
			briefs = append(briefs, SkillBrief{
				Name:             skill.Name,
				Level:            "full",
				Content:          skill.Content,
				RecommendedTools: skill.RecommendedTools,
			})

			builder.WriteString("[full] " + skill.Name + "\n" + skill.Content + "\n\n")
		}
	}

	if builder.Len() == 0 {
		return "", nil, nil
	}

	prefix := "Reference skill knowledge is background guidance only, " +
		"NOT observations from the current incident.\n" +
		"Never treat a skill as proof that its cause applies here.\n\n"

	return prefix + builder.String(), refs, briefs
}

// mergeSkillRefs 合并技能引用，按 (name, level) 去重并保持顺序。
func mergeSkillRefs(existing, added []SkillReference) []SkillReference {
	if len(added) == 0 {
		return existing
	}

	seen := make(map[string]struct{}, len(existing)+len(added))
	merged := make([]SkillReference, 0, len(existing)+len(added))

	for _, ref := range existing {
		key := ref.Name + "|" + ref.Level
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		merged = append(merged, ref)
	}

	for _, ref := range added {
		key := ref.Name + "|" + ref.Level
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		merged = append(merged, ref)
	}

	return merged
}

// parseLoadSkillName 从 load_skill 工具调用参数中解析技能名。
func parseLoadSkillName(arguments string) string {
	var input struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(arguments), &input); err != nil {
		return ""
	}
	return strings.TrimSpace(input.Name)
}
