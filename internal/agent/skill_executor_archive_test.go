package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestArchiveSkill(t *testing.T) {
	skillDir := t.TempDir()
	src := filepath.Join(skillDir, "old-skill")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte("body"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := ArchiveSkill(skillDir, "old-skill"); err != nil {
		t.Fatalf("ArchiveSkill: %v", err)
	}

	// 原位应空
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("原目录仍存在，归档失败")
	}

	// .archive/<ts>/old-skill 出现 + ListArchived 能列出
	list, err := ListArchived(skillDir)
	if err != nil {
		t.Fatalf("ListArchived: %v", err)
	}
	if len(list) != 1 || list[0].Name != "old-skill" {
		t.Errorf("archived = %+v, want [old-skill]", list)
	}

	// 幂等：再 archive 同名（已不在）不报错
	if err := ArchiveSkill(skillDir, "old-skill"); err != nil {
		t.Errorf("重复 archive 应幂等: %v", err)
	}
}
