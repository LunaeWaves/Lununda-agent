package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LunaeWaves/Lununda-agent/internal/provider"
	"github.com/LunaeWaves/Lununda-agent/internal/store"
)

type mockProvider struct{ content string }

func (m *mockProvider) Chat(ctx context.Context, messages []provider.Message, tools []provider.Tool, model string, maxTokens int, temperature float64) (*provider.Response, error) {
	return &provider.Response{Content: m.content}, nil
}
func (m *mockProvider) ChatStream(ctx context.Context, messages []provider.Message, tools []provider.Tool, model string, maxTokens int, temperature float64) (*provider.StreamReader, error) {
	return nil, nil
}

func newEvolutionTestStore(t *testing.T) store.Store {
	t.Helper()
	st, err := store.NewDBStore("sqlite", filepath.Join(t.TempDir(), "evolution.db"))
	if err != nil {
		t.Fatalf("NewDBStore: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		st.Close()
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// recordUsageAt 在 (user,agent,sess) 里 append nStep 条消息后记 skill usage。
func recordUsageAt(t *testing.T, st store.Store, ctx context.Context, user, agent, sess string, nStep int, skill, ts string) {
	t.Helper()
	for i := 0; i < nStep; i++ {
		if err := st.AppendSessionMessage(ctx, user, agent, sess, store.SessionMessage{Role: "user", Content: "x"}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if err := st.RecordSkillUsage(ctx, user, agent, sess, skill, ts); err != nil {
		t.Fatalf("record %s: %v", skill, err)
	}
}

func TestJudgePairRelevanceRelated(t *testing.T) {
	st := newEvolutionTestStore(t)
	ctx := context.Background()
	ts := time.Now().UTC().Format(time.RFC3339)

	// sess-A：先 append 3 条对话上下文，再记 pdf 和 docx（近距离）
	for _, msg := range []string{"帮我提取 PDF 文件", "好的，处理完了", "再帮我提取 DOCX"} {
		if err := st.AppendSessionMessage(ctx, "user-1", "agent-1", "sess-A", store.SessionMessage{Role: "user", Content: msg}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	recordUsageAt(t, st, ctx, "user-1", "agent-1", "sess-A", 1, "pdf-extract", ts)
	recordUsageAt(t, st, ctx, "user-1", "agent-1", "sess-A", 1, "docx-extract", ts)

	judger := &pairJudger{store: st, provider: &mockProvider{content: `{"verdict":"related","reason":"都是文档提取"}`}, model: "test"}
	verdict, _, err := judger.Judge(ctx, "agent-1", "docx-extract", "pdf-extract", 10)
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if verdict != "related" {
		t.Errorf("verdict = %q, want related", verdict)
	}
	if st.IsNotRelated(ctx, "agent-1", "docx-extract", "pdf-extract") {
		t.Errorf("应记 related，却被判 not_related")
	}
}

func TestParseVerdict(t *testing.T) {
	cases := []struct {
		in        string
		wantV     string
		wantR     string
		wantError bool
	}{
		{`{"verdict":"related","reason":"x"}`, "related", "x", false},
		{"```json\n{\"verdict\":\"not_related\",\"reason\":\"y\"}\n```", "not_related", "y", false},
		{"garbage", "", "", true},
		{`{"verdict":"maybe"}`, "", "", true},
	}
	for _, c := range cases {
		v, r, err := parseVerdict(c.in)
		if c.wantError {
			if err == nil {
				t.Errorf("parseVerdict(%q) 期望错误，got %q/%q", c.in, v, r)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseVerdict(%q) 意外错误: %v", c.in, err)
		}
		if v != c.wantV || r != c.wantR {
			t.Errorf("parseVerdict(%q) = %q/%q, want %q/%q", c.in, v, r, c.wantV, c.wantR)
		}
	}
}

func TestSynthesizeCluster(t *testing.T) {
	st := newEvolutionTestStore(t)
	ctx := context.Background()

	skillDir := filepath.Join(t.TempDir(), "skills")
	for _, name := range []string{"docx-extract", "pdf-extract", "xlsx-extract"} {
		dir := filepath.Join(skillDir, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		body := fmt.Sprintf("---\nname: %s\n---\n# %s\n提取 %s 文件内容\n", name, name, name)
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	synthBody := "---\nname: document-extract\ndescription: 类级文档提取\n---\n# 通用流程\n## PDF\n## DOCX\n## XLSX\n"
	synth := &clusterSynthesizer{store: st, provider: &mockProvider{content: synthBody}, model: "test"}
	id, err := synth.Synthesize(ctx, "agent-1", []string{"docx-extract", "pdf-extract", "xlsx-extract"}, skillDir, "3 sessions 共用")
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if id == "" {
		t.Fatal("proposal id 为空")
	}

	pending, _ := st.ListPendingProposals(ctx, "agent-1")
	var found *store.SkillProposal
	for i := range pending {
		if pending[i].ID == id {
			found = &pending[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("未找到提案 %s", id)
	}
	if found.TargetName != "document-extract" {
		t.Errorf("TargetName = %q, want document-extract", found.TargetName)
	}
	if !strings.Contains(found.TargetContent, "# 通用流程") {
		t.Errorf("TargetContent 缺少综合正文： %q", found.TargetContent)
	}
}

func TestParseFrontmatterName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"---\nname: foo\n---\n# body", "foo"},
		{"---\nname:bar\n---\n", "bar"},
		{"no frontmatter", ""},
		{"---\ndescription: x\nname: real\n---\n", "real"},
	}
	for _, c := range cases {
		got := parseFrontmatterName(c.in)
		if got != c.want {
			t.Errorf("parseFrontmatterName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
