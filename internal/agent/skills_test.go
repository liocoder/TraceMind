package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liocoder/TraceMind/internal/skills"
)

const testPoolSkill = `---
name: connection_pool
description: 排查连接池耗尽
triggers: connection pool, 连接池
recommended_tools: query_metrics, search_logs
---

# 连接池耗尽

查询活跃连接数与等待连接数。
`

const testDBLatencySkill = `---
name: database_latency
description: 排查数据库延迟
triggers: slow query, 慢查询
recommended_tools: query_metrics
---

# 数据库延迟

查询 P95 延迟。
`

func writeSkillFiles(t *testing.T, files map[string]string) *skills.Library {
	t.Helper()

	root := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write skill %s: %v", name, err)
		}
	}

	lib, err := skills.Load(root)
	if err != nil {
		t.Fatalf("load skills: %v", err)
	}
	return lib
}

func TestSkillLoaderEvictsByLRU(t *testing.T) {
	loader := newSkillLoader(2)

	if got := loader.Load("connection_pool"); got != "" {
		t.Fatalf("first load should not evict, got %q", got)
	}
	if got := loader.Load("database_latency"); got != "" {
		t.Fatalf("second load should not evict, got %q", got)
	}
	if got := loader.Load("downstream_timeout"); got != "connection_pool" {
		t.Fatalf("expected eviction of connection_pool, got %q", got)
	}

	got := loader.Loaded()
	want := []string{"database_latency", "downstream_timeout"}
	if len(got) != len(want) {
		t.Fatalf("loaded skills = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("loaded skills = %v, want %v", got, want)
		}
	}
}

func TestSkillLoaderReloadRefreshesLRU(t *testing.T) {
	loader := newSkillLoader(2)
	loader.Load("a")
	loader.Load("b")
	loader.Load("a")

	if got := loader.Load("c"); got != "b" {
		t.Fatalf("expected eviction of b after refreshing a, got %q", got)
	}
}

func TestBuildSkillContextMatchesAndLoads(t *testing.T) {
	lib := writeSkillFiles(t, map[string]string{
		"pool.md": testPoolSkill,
		"db.md":   testDBLatencySkill,
	})

	loader := newSkillLoader(maxLoadedSkills)

	message, refs, briefs := buildSkillContext(
		lib,
		"order service connection pool exhausted, slow queries observed",
		loader,
	)
	if message == "" {
		t.Fatal("expected a skill context message")
	}
	if !strings.Contains(message, "connection_pool") {
		t.Fatalf("message should mention connection_pool:\n%s", message)
	}

	var summarySeen, fullSeen bool
	for _, ref := range refs {
		switch {
		case ref.Name == "connection_pool" && ref.Level == "summary":
			summarySeen = true
		case ref.Name == "connection_pool" && ref.Level == "full":
			fullSeen = true
		}
	}
	if !summarySeen {
		t.Fatal("expected a summary-level reference for connection_pool")
	}
	if fullSeen {
		t.Fatal("no skill should be full before load_skill is called")
	}

	foundBrief := false
	for _, brief := range briefs {
		if brief.Name == "connection_pool" && brief.Level == "summary" {
			foundBrief = true
		}
	}
	if !foundBrief {
		t.Fatal("expected a summary SkillBrief for connection_pool")
	}
}

func TestBuildSkillContextIncludesLoadedFullContent(t *testing.T) {
	lib := writeSkillFiles(t, map[string]string{
		"pool.md": testPoolSkill,
	})

	loader := newSkillLoader(maxLoadedSkills)
	loader.Load("connection_pool")

	message, refs, _ := buildSkillContext(lib, "nothing matches here", loader)
	if !strings.Contains(message, "[full] connection_pool") {
		t.Fatalf("expected full content marker in message:\n%s", message)
	}

	found := false
	for _, ref := range refs {
		if ref.Name == "connection_pool" && ref.Level == "full" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected a full-level reference for connection_pool")
	}
}

func TestBuildSkillContextNoMatchNoLoad(t *testing.T) {
	lib := writeSkillFiles(t, map[string]string{
		"pool.md": testPoolSkill,
	})

	message, refs, briefs := buildSkillContext(
		lib,
		"kafka consumer lag is rising",
		newSkillLoader(maxLoadedSkills),
	)
	if message != "" || len(refs) != 0 || len(briefs) != 0 {
		t.Fatalf("expected empty context for unrelated query, got %q %v %v", message, refs, briefs)
	}
}

func TestMergeSkillRefsDeduplicates(t *testing.T) {
	merged := mergeSkillRefs(
		[]SkillReference{{Name: "a", Level: "summary"}},
		[]SkillReference{
			{Name: "a", Level: "summary"},
			{Name: "a", Level: "full"},
			{Name: "b", Level: "summary"},
		},
	)

	if len(merged) != 3 {
		t.Fatalf("expected 3 unique refs, got %+v", merged)
	}
}

func TestParseLoadSkillName(t *testing.T) {
	for _, test := range []struct {
		in   string
		want string
	}{
		{`{"name": "connection_pool"}`, "connection_pool"},
		{`{ "name" : "database_latency" }`, "database_latency"},
		{`{"other": 1}`, ""},
		{``, ""},
	} {
		if got := parseLoadSkillName(test.in); got != test.want {
			t.Fatalf("parseLoadSkillName(%q) = %q, want %q", test.in, got, test.want)
		}
	}
}
