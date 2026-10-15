package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testSkillConnectionPool = `---
name: connection_pool
description: 排查数据库连接池耗尽导致的请求超时
triggers: connection pool, connection_pool, 连接池, 连接耗尽
recommended_tools: [query_metrics, search_logs]
---

# 连接池耗尽导致请求超时

## 调查步骤

1. 查询连接池活跃连接数。
2. 搜索连接获取超时日志。
`

const testSkillDatabaseLatency = `---
name: database_latency
description: 排查数据库延迟升高与慢查询
triggers: slow query, database, 慢查询, 数据库
recommended_tools: query_metrics, search_logs
---

# 数据库延迟升高

查询 P95 延迟并对比业务超时时间点。
`

func writeTestSkills(t *testing.T, files map[string]string) string {
	t.Helper()

	root := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write skill %s: %v", name, err)
		}
	}
	return root
}

func TestLoadParsesFrontMatter(t *testing.T) {
	root := writeTestSkills(t, map[string]string{
		"connection-pool.md": testSkillConnectionPool,
	})

	lib, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	skill, ok := lib.Get("connection_pool")
	if !ok {
		t.Fatal("expected skill connection_pool to exist")
	}
	if skill.Description == "" {
		t.Fatal("expected a non-empty description")
	}
	if len(skill.Triggers) != 4 {
		t.Fatalf("expected 4 triggers, got %v", skill.Triggers)
	}
	if len(skill.RecommendedTools) != 2 {
		t.Fatalf("expected 2 recommended tools, got %v", skill.RecommendedTools)
	}
	if !strings.Contains(skill.Content, "查询连接池活跃连接数") {
		t.Fatal("expected front-matter to be stripped from content")
	}
	if strings.Contains(skill.Content, "recommended_tools") {
		t.Fatal("front-matter should not appear in content")
	}
}

func TestLoadRejectsMissingName(t *testing.T) {
	root := writeTestSkills(t, map[string]string{
		"bad.md": `---
description: no name here
triggers: a
---

body`,
	})

	if _, err := Load(root); err == nil {
		t.Fatal("expected an error for a skill without a name")
	}
}

func TestLoadRejectsDuplicateName(t *testing.T) {
	root := writeTestSkills(t, map[string]string{
		"a.md": testSkillConnectionPool,
		"b.md": testSkillConnectionPool,
	})

	if _, err := Load(root); err == nil {
		t.Fatal("expected an error for duplicate skill names")
	}
}

func TestLoadRejectsNoSkills(t *testing.T) {
	root := t.TempDir()

	if _, err := Load(root); err == nil {
		t.Fatal("expected an error for an empty skills directory")
	}
}

func TestIndexContainsAllSkills(t *testing.T) {
	root := writeTestSkills(t, map[string]string{
		"a.md": testSkillConnectionPool,
		"b.md": testSkillDatabaseLatency,
	})

	lib, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	index := lib.Index()
	for _, name := range []string{"connection_pool", "database_latency"} {
		if !strings.Contains(index, name) {
			t.Fatalf("index should mention %q:\n%s", name, index)
		}
	}
	if !strings.Contains(index, "load_skill") {
		t.Fatal("index should mention the load_skill tool")
	}
}

func TestMatchByEnglishTrigger(t *testing.T) {
	root := writeTestSkills(t, map[string]string{
		"a.md": testSkillConnectionPool,
		"b.md": testSkillDatabaseLatency,
	})

	lib, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	matched := lib.Match("The logs report connection pool exhaustion")
	if len(matched) != 1 {
		t.Fatalf("expected exactly 1 match, got %d: %+v", len(matched), matched)
	}
	if matched[0].Name != "connection_pool" {
		t.Fatalf("expected connection_pool, got %q", matched[0].Name)
	}
}

func TestMatchByChineseTrigger(t *testing.T) {
	root := writeTestSkills(t, map[string]string{
		"a.md": testSkillConnectionPool,
	})

	lib, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	matched := lib.Match("订单服务连接池耗尽导致超时")
	if len(matched) != 1 || matched[0].Name != "connection_pool" {
		t.Fatalf("expected Chinese trigger match, got %+v", matched)
	}
}

func TestMatchCaseInsensitive(t *testing.T) {
	root := writeTestSkills(t, map[string]string{
		"a.md": testSkillConnectionPool,
	})

	lib, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got := lib.Match("CONNECTION POOL exhausted"); len(got) != 1 {
		t.Fatalf("expected case-insensitive match, got %d", len(got))
	}
}

func TestMatchNoHit(t *testing.T) {
	root := writeTestSkills(t, map[string]string{
		"a.md": testSkillConnectionPool,
	})

	lib, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got := lib.Match("kafka consumer lag is rising"); len(got) != 0 {
		t.Fatalf("expected no match for unrelated query, got %+v", got)
	}
}

func TestSkillContent(t *testing.T) {
	root := writeTestSkills(t, map[string]string{
		"a.md": testSkillConnectionPool,
	})

	lib, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	content, ok := lib.SkillContent("connection_pool")
	if !ok || !strings.Contains(content, "调查步骤") {
		t.Fatalf("SkillContent: ok=%v content=%q", ok, content)
	}

	if _, ok := lib.SkillContent("no_such_skill"); ok {
		t.Fatal("expected missing skill to report not found")
	}
}
