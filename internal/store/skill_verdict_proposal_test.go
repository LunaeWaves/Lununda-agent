package store

import (
	"context"
	"testing"
)

func TestPairVerdictUpsertAndQuery(t *testing.T) {
	d := setupTestDB(t)
	ctx := context.Background()

	if err := d.RecordPairVerdict(ctx, "agent-1", "docx-extract", "pdf-extract", "related", "same doc class", "2026-06-21T00:00:00Z"); err != nil {
		t.Fatalf("RecordPairVerdict related: %v", err)
	}
	if err := d.RecordPairVerdict(ctx, "agent-1", "pdf-extract", "xlsx-extract", "not_related", "different tools", "2026-06-21T00:00:00Z"); err != nil {
		t.Fatalf("RecordPairVerdict not_related: %v", err)
	}

	// 重复裁决（同 pair）应 UPSERT 更新而非报错
	if err := d.RecordPairVerdict(ctx, "agent-1", "pdf-extract", "xlsx-extract", "not_related", "updated reason", "2026-06-21T01:00:00Z"); err != nil {
		t.Errorf("重复裁决应 UPSERT: %v", err)
	}

	rel, err := d.ListRelatedPairs(ctx, "agent-1")
	if err != nil {
		t.Fatalf("ListRelatedPairs: %v", err)
	}
	if len(rel) != 1 || !pairIs(rel[0], "docx-extract", "pdf-extract") {
		t.Errorf("related pair = %+v，want 仅 docx/pdf", rel)
	}

	// 归一化：(a,b) 与 (b,a) 应判同一 pair
	if !d.IsNotRelated(ctx, "agent-1", "pdf-extract", "xlsx-extract") {
		t.Errorf("pdf/xlsx 应判 not_related")
	}
	if d.IsNotRelated(ctx, "agent-1", "docx-extract", "pdf-extract") {
		t.Errorf("docx/pdf 是 related，不应判 not_related")
	}

	// reason 应是 UPSERT 后的最新值
	var reason string
	d.DB().QueryRowContext(ctx,
		`SELECT reason FROM skill_pair_verdict WHERE agent_id='agent-1' AND skill_a='pdf-extract' AND skill_b='xlsx-extract'`).Scan(&reason)
	if reason != "updated reason" {
		t.Errorf("reason = %q, want 'updated reason'（UPSERT 后）", reason)
	}
}

func pairIs(p SkillPair, a, b string) bool {
	return (p.A == a && p.B == b) || (p.A == b && p.B == a)
}

func TestProposalLifecycle(t *testing.T) {
	d := setupTestDB(t)
	ctx := context.Background()

	id, err := d.CreateProposal(ctx, &SkillProposal{
		AgentID: "agent-1", Sources: []string{"docx-extract", "pdf-extract"},
		TargetName: "document-extract", TargetContent: "---\nname: document-extract\n---\nbody",
		Evidence: "2 sessions, avg dist 1", Recommendation: "merge",
		CreatedAt: "2026-06-21T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("CreateProposal: %v", err)
	}
	if id == "" {
		t.Fatal("proposal id 为空")
	}

	pending, err := d.ListPendingProposals(ctx, "agent-1")
	if err != nil {
		t.Fatalf("ListPendingProposals: %v", err)
	}
	if len(pending) != 1 || pending[0].TargetName != "document-extract" {
		t.Errorf("pending = %+v", pending)
	}
	if len(pending[0].Sources) != 2 || pending[0].Sources[0] != "docx-extract" {
		t.Errorf("Sources 反序列化错误： %+v", pending[0].Sources)
	}

	if err := d.SetProposalStatus(ctx, id, "rejected", "2026-06-21T01:00:00Z"); err != nil {
		t.Fatalf("SetProposalStatus rejected: %v", err)
	}
	pending2, _ := d.ListPendingProposals(ctx, "agent-1")
	if len(pending2) != 0 {
		t.Errorf("rejected 后 pending 应空，got %d", len(pending2))
	}
}
