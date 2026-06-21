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

// routingProvider routes Chat calls by inspecting the last user message:
// relevance prompts (containing "判断") get a JSON verdict, synthesis
// prompts (containing "综合") get a SKILL.md body. Lets one end-to-end
// orchestrator test exercise both段 2 and 段 3 without two separate stubs.
type routingProvider struct {
	verdictResp string
	synthResp   string
}

func (p *routingProvider) Chat(ctx context.Context, messages []provider.Message, tools []provider.Tool, model string, maxTokens int, temperature float64) (*provider.Response, error) {
	last := ""
	if len(messages) > 0 {
		last = messages[len(messages)-1].Content
	}
	if strings.Contains(last, "综合") {
		return &provider.Response{Content: p.synthResp}, nil
	}
	return &provider.Response{Content: p.verdictResp}, nil
}
func (p *routingProvider) ChatStream(ctx context.Context, messages []provider.Message, tools []provider.Tool, model string, maxTokens int, temperature float64) (*provider.StreamReader, error) {
	return nil, nil
}

func TestRunSkillEvolutionEndToEnd(t *testing.T) {
	st := newEvolutionTestStore(t)
	ctx := context.Background()
	ts := time.Now().UTC().Format(time.RFC3339)

	// 跨 3 个 session 近距离共用 pdf+docx；csv 单独一次（不够 minSessions=3 门槛）
	for i, sess := range []string{"sess-A", "sess-B", "sess-C"} {
		userID := "user-" + string(rune('1'+i))
		for _, msg := range []string{"帮我提取 PDF", "再提取 DOCX"} {
			st.AppendSessionMessage(ctx, userID, "agent-1", sess, store.SessionMessage{Role: "user", Content: msg})
		}
		recordUsageAt(t, st, ctx, userID, "agent-1", sess, 1, "pdf-extract", ts)
		recordUsageAt(t, st, ctx, userID, "agent-1", sess, 1, "docx-extract", ts)
	}

	skillDir := filepath.Join(t.TempDir(), "skills")
	for _, name := range []string{"pdf-extract", "docx-extract"} {
		dir := filepath.Join(skillDir, name)
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(fmt.Sprintf("---\nname: %s\n---\n# %s\n", name, name)), 0o644)
	}

	prov := &routingProvider{
		verdictResp: `{"verdict":"related","reason":"都是文档提取"}`,
		synthResp:   "---\nname: document-extract\ndescription: 类级\n---\n# 通用文档提取\n## PDF\n## DOCX\n",
	}
	ev := &skillEvolution{
		store: st, provider: prov, model: "test", skillDir: skillDir,
		maxDistance: 10, minSessions: 3,
	}
	ids, err := ev.Run(ctx, "agent-1")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("proposals = %d, want 1 (got: %v)", len(ids), ids)
	}
	pending, _ := st.ListPendingProposals(ctx, "agent-1")
	if len(pending) != 1 || pending[0].TargetName != "document-extract" {
		t.Errorf("pending = %+v", pending)
	}

	// 二次 Run 应幂等：verdict 已存在，不重复 LLM；但会再产 1 个 proposal
	// （BuildClusters 仍返回 {pdf,docx}）。补加 IsNotRelated/HasVerdict 防护即可。
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

// TestSkillEvolutionFullFlowAudit exercises the full curator chain that
// reviewer issue #8 flagged as missing: Run → ApplyProposal → archive →
// ListArchived. Asserts the合成→接受→归档可恢复 loop behaves end-to-end
// with a mock provider.
func TestSkillEvolutionFullFlowAudit(t *testing.T) {
	st := newEvolutionTestStore(t)
	ctx := context.Background()
	ts := time.Now().UTC().Format(time.RFC3339)

	for i, sess := range []string{"s-A", "s-B", "s-C"} {
		userID := "u-" + string(rune('1'+i))
		st.AppendSessionMessage(ctx, userID, "agent-X", sess, store.SessionMessage{Role: "user", Content: "pdf"})
		st.AppendSessionMessage(ctx, userID, "agent-X", sess, store.SessionMessage{Role: "user", Content: "docx"})
		recordUsageAt(t, st, ctx, userID, "agent-X", sess, 1, "pdf-extract", ts)
		recordUsageAt(t, st, ctx, userID, "agent-X", sess, 1, "docx-extract", ts)
	}
	skillDir := filepath.Join(t.TempDir(), "skills")
	for _, name := range []string{"pdf-extract", "docx-extract"} {
		dir := filepath.Join(skillDir, name)
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: "+name+"\n---\n# "+name+"\n"), 0o644)
	}

	ev := &skillEvolution{
		store: st, provider: &routingProvider{
			verdictResp: `{"verdict":"related","reason":"doc class"}`,
			synthResp:   "---\nname: document-extract\ndescription: 类级\n---\n# 通用\n## PDF\n## DOCX\n",
		},
		model: "test", skillDir: skillDir, maxDistance: 10, minSessions: 3,
	}
	ids, err := ev.Run(ctx, "agent-X")
	if err != nil || len(ids) != 1 {
		t.Fatalf("Run: err=%v ids=%v", err, ids)
	}

	// Accept — keep none of the sources so both get archived.
	if err := ApplyProposal(ctx, st, ids[0], nil, skillDir); err != nil {
		t.Fatalf("ApplyProposal: %v", err)
	}

	// proposal status flipped to applied
	p, _ := st.GetProposal(ctx, ids[0])
	if p.Status != "applied" {
		t.Errorf("proposal status = %q, want applied", p.Status)
	}
	// new target exists
	if _, err := os.Stat(filepath.Join(skillDir, "document-extract", "SKILL.md")); err != nil {
		t.Errorf("target skill missing: %v", err)
	}
	// both sources archived (not at top level)
	for _, name := range []string{"pdf-extract", "docx-extract"} {
		if _, err := os.Stat(filepath.Join(skillDir, name)); !os.IsNotExist(err) {
			t.Errorf("source %s still at top level (should be archived)", name)
		}
	}
	// archive listed
	arch, err := ListArchived(skillDir)
	if err != nil {
		t.Fatalf("ListArchived: %v", err)
	}
	if len(arch) != 2 {
		t.Errorf("archived = %d items, want 2: %+v", len(arch), arch)
	}
}
