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

// ApplyProposal must refuse to overwrite an already-existing target skill dir
// so a benign name collision can't silently destroy a user skill.
func TestApplyProposalRejectsExistingTarget(t *testing.T) {
	st := newEvolutionTestStore(t)
	ctx := context.Background()
	pid, err := st.CreateProposal(ctx, &store.SkillProposal{
		AgentID:       "agent-1",
		Sources:       []string{"a"},
		TargetName:    "existing",
		TargetContent: "body",
		Status:        "pending",
		CreatedAt:     "2026-06-21T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("CreateProposal: %v", err)
	}
	skillDir := t.TempDir()
	target := filepath.Join(skillDir, "existing")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	orig := []byte("# 我的现有技能")
	if err := os.WriteFile(filepath.Join(target, "SKILL.md"), orig, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	err = ApplyProposal(ctx, st, pid, nil, skillDir)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("ApplyProposal(existing target) err = %v, want already exists", err)
	}
	got, readErr := os.ReadFile(filepath.Join(target, "SKILL.md"))
	if readErr != nil || string(got) != string(orig) {
		t.Errorf("existing skill overwritten: readErr=%v got=%q", readErr, got)
	}
}
