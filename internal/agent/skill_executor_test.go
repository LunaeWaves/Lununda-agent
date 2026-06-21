package agent

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/LunaeWaves/Lununda-agent/internal/store"
)

func TestApplyProposalWritesAndArchives(t *testing.T) {
	skillDir := t.TempDir()
	for _, name := range []string{"docx-extract", "pdf-extract"} {
		os.MkdirAll(filepath.Join(skillDir, name), 0o755)
		os.WriteFile(filepath.Join(skillDir, name, "SKILL.md"), []byte("# "+name), 0o644)
	}
	st := newEvolutionTestStore(t)
	ctx := context.Background()
	id, err := st.CreateProposal(ctx, &store.SkillProposal{
		AgentID: "agent-1", Sources: []string{"docx-extract", "pdf-extract"},
		TargetName: "document-extract", TargetContent: "---\nname: document-extract\n---\n合并体",
		Status: "pending", CreatedAt: "2026-06-21T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("CreateProposal: %v", err)
	}

	if err := ApplyProposal(ctx, st, id, []string{"pdf-extract"}, skillDir); err != nil {
		t.Fatalf("ApplyProposal: %v", err)
	}

	if _, err := os.Stat(filepath.Join(skillDir, "document-extract", "SKILL.md")); err != nil {
		t.Errorf("新技能未写入: %v", err)
	}
	if _, err := os.Stat(filepath.Join(skillDir, "pdf-extract", "SKILL.md")); err != nil {
		t.Errorf("保留的 pdf-extract 不见了: %v", err)
	}
	if _, err := os.Stat(filepath.Join(skillDir, "docx-extract")); !os.IsNotExist(err) {
		t.Errorf("未保留的 docx-extract 应已移走，仍存在")
	}
	archived := findArchived(t, skillDir, "docx-extract")
	if archived == "" {
		t.Errorf("docx-extract 未在 .archive 中找到")
	}
	p, _ := st.GetProposal(ctx, id)
	if p.Status != "applied" {
		t.Errorf("status = %q, want applied", p.Status)
	}
}

func TestListArchivedAndDelete(t *testing.T) {
	skillDir := t.TempDir()
	// 造两个时间戳目录，各放一个归档技能
	ts1 := "20260101-120000"
	ts2 := "20260102-120000"
	for _, td := range []struct{ ts, skill string }{
		{ts1, "old-skill"},
		{ts2, "newer-skill"},
	} {
		dir := filepath.Join(skillDir, ".archive", td.ts, td.skill)
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# "+td.skill), 0o644)
	}

	list, err := ListArchived(skillDir)
	if err != nil {
		t.Fatalf("ListArchived: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("archived = %d, want 2: %+v", len(list), list)
	}
	// 排序：newest first
	if list[0].ArchivedAt != ts2 {
		t.Errorf("ArchivedAt[0] = %q, want %q (newest first)", list[0].ArchivedAt, ts2)
	}

	if err := DeleteArchivedSkill(skillDir, ts1, "old-skill"); err != nil {
		t.Fatalf("DeleteArchivedSkill: %v", err)
	}
	list2, _ := ListArchived(skillDir)
	if len(list2) != 1 || list2[0].Name != "newer-skill" {
		t.Errorf("after delete = %+v", list2)
	}
}

func TestListArchivedMissingDir(t *testing.T) {
	list, err := ListArchived(t.TempDir())
	if err != nil {
		t.Errorf("missing .archive 应返回 nil 无错: %v", err)
	}
	if list != nil {
		t.Errorf("missing .archive 应返回 nil slice，got %+v", list)
	}
}

func findArchived(t *testing.T, skillDir, name string) string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(skillDir, ".archive"))
	if err != nil {
		return ""
	}
	var tsDirs []string
	for _, e := range entries {
		if e.IsDir() {
			tsDirs = append(tsDirs, e.Name())
		}
	}
	sort.Strings(tsDirs)
	for _, ts := range tsDirs {
		if _, err := os.Stat(filepath.Join(skillDir, ".archive", ts, name)); err == nil {
			return ts
		}
	}
	return ""
}
