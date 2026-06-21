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
