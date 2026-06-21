package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LunaeWaves/Lununda-agent/internal/store"
)

// Regression: spec D4 requires ApplyProposal / ArchiveSkill / DeleteArchivedSkill
// to reject path-traversal in LLM-derived names. Each attack vector below must
// be refused before any filesystem write.
func TestPathTraversalRejected(t *testing.T) {
	cases := []string{"../etc", "..", ".", "foo/bar", `foo\bar`, "a/../b", "/abs"}
	for _, name := range cases {
		if isSafeSkillName(name) {
			t.Errorf("isSafeSkillName(%q) = true, want false", name)
		}
	}
	good := []string{"pdf-extract", "docx_extract", "a.b.c", "skill-1"}
	for _, name := range good {
		if !isSafeSkillName(name) {
			t.Errorf("isSafeSkillName(%q) = false, want true", name)
		}
	}
}

// ArchiveSkill with a traversal name must fail without touching FS.
func TestArchiveSkillRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	if err := ArchiveSkill(dir, "../escaped"); err == nil {
		t.Errorf("ArchiveSkill traversal accepted (should be rejected)")
	}
	if _, err := os.Stat(filepath.Join(dir, "..", "escaped")); err == nil {
		t.Errorf("traversal escaped skillDir — file created outside")
	}
}

// DeleteArchivedSkill with traversal name must fail without RemoveAll escape.
func TestDeleteArchivedSkillRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	if err := DeleteArchivedSkill(dir, "..", "anything"); err == nil {
		t.Errorf("DeleteArchivedSkill traversal accepted")
	}
	if err := DeleteArchivedSkill(dir, "ok-ts", "../up"); err == nil {
		t.Errorf("DeleteArchivedSkill name traversal accepted")
	}
}

// ApplyProposal must reject unsafe TargetName before touching the FS.
func TestApplyProposalRejectsUnsafeName(t *testing.T) {
	st := newEvolutionTestStore(t)
	ctx := context.Background()
	pid, err := st.CreateProposal(ctx, &store.SkillProposal{
		AgentID:       "agent-1",
		Sources:       []string{"a", "b"},
		TargetName:    "../pwned",
		TargetContent: "body",
		Status:        "pending",
		CreatedAt:     "2026-06-21T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("CreateProposal: %v", err)
	}
	skillDir := t.TempDir()
	err = ApplyProposal(ctx, st, pid, nil, skillDir)
	if err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Errorf("ApplyProposal(traversal target) err = %v, want unsafe", err)
	}
}
