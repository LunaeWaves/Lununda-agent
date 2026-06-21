package store

import (
	"context"
	"testing"
)

// TestRecordSkillUsageDerivesSeqFromSessionMessages:
// AppendSessionMessage 自动分配 seq；RecordSkillUsage 应把当前对话深度（MAX(seq)）记进去。
func TestRecordSkillUsageDerivesSeqFromSessionMessages(t *testing.T) {
	d := setupTestDB(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := d.AppendSessionMessage(ctx, "user-1", "agent-1", "sess-A", SessionMessage{Role: "user", Content: "x"}); err != nil {
			t.Fatalf("append msg %d: %v", i, err)
		}
	}
	if err := d.RecordSkillUsage(ctx, "user-1", "agent-1", "sess-A", "pdf-extract", "2026-06-21T00:00:00Z"); err != nil {
		t.Fatalf("RecordSkillUsage: %v", err)
	}

	var seq int
	if err := d.DB().QueryRowContext(ctx,
		`SELECT seq FROM skill_usage WHERE user_id='user-1' AND agent_id='agent-1' AND session_key='sess-A' AND skill_id='pdf-extract'`).Scan(&seq); err != nil {
		t.Fatalf("query skill_usage: %v", err)
	}
	if seq != 2 {
		t.Errorf("派生 seq = %d, want 2（当前对话深度，0-indexed）", seq)
	}

	// 重复记（同 user/agent/session/seq/skill）应被 ON CONFLICT 忽略，不报错也不新增
	if err := d.RecordSkillUsage(ctx, "user-1", "agent-1", "sess-A", "pdf-extract", "2026-06-21T00:00:00Z"); err != nil {
		t.Errorf("重复记应忽略而非报错: %v", err)
	}
	var n int
	d.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM skill_usage WHERE agent_id='agent-1'`).Scan(&n)
	if n != 1 {
		t.Errorf("重复记后行数 = %d, want 1", n)
	}
}

// recordAt：在 (user,agent,sess) 里先把对话深度推进 nStep（append nStep 条消息），
// 再记 skill。使不同 skill 落在不同 seq，距离 = step 差。
func recordAt(t *testing.T, d *DBStore, ctx context.Context, user, agent, sess string, nStep int, skill string) {
	t.Helper()
	for i := 0; i < nStep; i++ {
		if err := d.AppendSessionMessage(ctx, user, agent, sess, SessionMessage{Role: "user", Content: "x"}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if err := d.RecordSkillUsage(ctx, user, agent, sess, skill, "2026-06-21T00:00:00Z"); err != nil {
		t.Fatalf("record %s: %v", skill, err)
	}
}

func TestCandidateSkillPairsCountsDistinctSessions(t *testing.T) {
	d := setupTestDB(t)
	ctx := context.Background()

	// sess-A（user-1）：pdf@0, docx@1 → 距离 1
	recordAt(t, d, ctx, "user-1", "agent-1", "sess-A", 1, "pdf-extract")
	recordAt(t, d, ctx, "user-1", "agent-1", "sess-A", 1, "docx-extract")
	// sess-B（user-2，不同 session）：pdf@0, docx@1 → 又一次近距离共用
	recordAt(t, d, ctx, "user-2", "agent-1", "sess-B", 1, "pdf-extract")
	recordAt(t, d, ctx, "user-2", "agent-1", "sess-B", 1, "docx-extract")
	// sess-C：pdf@0, xlsx@49 → 距离 49，应被距离过滤掉
	recordAt(t, d, ctx, "user-3", "agent-1", "sess-C", 1, "pdf-extract")
	recordAt(t, d, ctx, "user-3", "agent-1", "sess-C", 49, "xlsx-extract")

	pairs, err := d.CandidateSkillPairs(ctx, "agent-1", 10, 2)
	if err != nil {
		t.Fatalf("CandidateSkillPairs: %v", err)
	}
	if len(pairs) == 0 {
		t.Fatal("期望至少 1 个候选对，got 0")
	}
	top := pairs[0]
	pair := map[string]bool{top.SkillA: true, top.SkillB: true}
	if !pair["pdf-extract"] || !pair["docx-extract"] {
		t.Errorf("首选应对是 pdf/docx，got %s/%s", top.SkillA, top.SkillB)
	}
	if top.Sessions != 2 {
		t.Errorf("pdf/docx Sessions = %d, want 2（跨 2 个不同 session）", top.Sessions)
	}
	for _, p := range pairs {
		s := map[string]bool{p.SkillA: true, p.SkillB: true}
		if s["pdf-extract"] && s["xlsx-extract"] {
			t.Errorf("pdf/xlsx 距离过大不应成候选： %+v", p)
		}
	}
}
