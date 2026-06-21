package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/LunaeWaves/Lununda-agent/internal/store"
)

// ApplyProposal executes an accepted proposal: writes the new target skill,
// archives sources the user did not keep, then marks the proposal "applied".
// keepSources = source skill ids the user wants to retain unchanged.
// Already-absent sources are skipped (idempotent re-applies).
//
// Target / source names are LLM-derived (frontmatter / Sources list) —
// reject anything that would escape skillDir via traversal.
func ApplyProposal(ctx context.Context, st store.Store, proposalID string, keepSources []string, skillDir string) error {
	p, err := st.GetProposal(ctx, proposalID)
	if err != nil {
		return fmt.Errorf("get proposal: %w", err)
	}
	if p.Status != "pending" && p.Status != "accepted" {
		return fmt.Errorf("proposal not applicable (status=%s)", p.Status)
	}
	if !isSafeSkillName(p.TargetName) {
		return fmt.Errorf("unsafe target name: %q", p.TargetName)
	}
	for _, s := range p.Sources {
		if !isSafeSkillName(s) {
			return fmt.Errorf("unsafe source name: %q", s)
		}
	}
	keep := map[string]bool{}
	for _, s := range keepSources {
		keep[s] = true
	}

	targetDir := filepath.Join(skillDir, p.TargetName)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("mkdir target: %w", err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "SKILL.md"), []byte(p.TargetContent), 0o644); err != nil {
		return fmt.Errorf("write target SKILL.md: %w", err)
	}

	ts := time.Now().UTC().Format("20060102-150405")
	archiveRoot := filepath.Join(skillDir, ".archive", ts)
	for _, src := range p.Sources {
		if keep[src] {
			continue
		}
		srcDir := filepath.Join(skillDir, src)
		if _, err := os.Stat(srcDir); os.IsNotExist(err) {
			continue
		}
		if err := os.MkdirAll(archiveRoot, 0o755); err != nil {
			return fmt.Errorf("mkdir archive: %w", err)
		}
		if err := os.Rename(srcDir, filepath.Join(archiveRoot, src)); err != nil {
			return fmt.Errorf("archive %s: %w", src, err)
		}
	}

	return st.SetProposalStatus(ctx, proposalID, "applied", time.Now().UTC().Format(time.RFC3339))
}

// ArchivedSkill is one entry under <skillDir>/.archive/<ts>/<name>/.
type ArchivedSkill struct {
	Name       string `json:"name"`
	ArchivedAt string `json:"archivedAt"` // timestamp dir name (YYYYMMDD-HHMMSS)
	Path       string `json:"path"`
}

// ListArchived scans <skillDir>/.archive/*/ and returns every archived skill,
// newest first. Returns nil (no error) if the archive dir doesn't exist yet.
func ListArchived(skillDir string) ([]ArchivedSkill, error) {
	archiveBase := filepath.Join(skillDir, ".archive")
	entries, err := os.ReadDir(archiveBase)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var tsDirs []os.DirEntry
	for _, e := range entries {
		if e.IsDir() {
			tsDirs = append(tsDirs, e)
		}
	}
	sort.Slice(tsDirs, func(i, j int) bool { return tsDirs[i].Name() > tsDirs[j].Name() })

	var out []ArchivedSkill
	for _, tsDir := range tsDirs {
		skills, err := os.ReadDir(filepath.Join(archiveBase, tsDir.Name()))
		if err != nil {
			continue
		}
		for _, s := range skills {
			if !s.IsDir() {
				continue
			}
			out = append(out, ArchivedSkill{
				Name:       s.Name(),
				ArchivedAt: tsDir.Name(),
				Path:       filepath.Join(archiveBase, tsDir.Name(), s.Name()),
			})
		}
	}
	return out, nil
}

// DeleteArchivedSkill permanently removes one archived skill dir. Curator
// never calls this — only the user via the dashboard "delete forever" button.
func DeleteArchivedSkill(skillDir, archivedAt, name string) error {
	if !isSafeSkillName(name) || !isSafeSkillName(archivedAt) {
		return fmt.Errorf("unsafe name")
	}
	return os.RemoveAll(filepath.Join(skillDir, ".archive", archivedAt, name))
}

// ArchiveSkill moves a single skill dir into .archive/<ts>/<name>/ for
// later restore or manual cleanup. Powers stale-skill archival and the
// manual "archive" button. Idempotent: missing source is a no-op.
func ArchiveSkill(skillDir, name string) error {
	if !isSafeSkillName(name) {
		return fmt.Errorf("unsafe skill name: %q", name)
	}
	src := filepath.Join(skillDir, name)
	if _, err := os.Stat(src); os.IsNotExist(err) {
		return nil
	}
	ts := time.Now().UTC().Format("20060102-150405")
	dst := filepath.Join(skillDir, ".archive", ts, name)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("mkdir archive: %w", err)
	}
	return os.Rename(src, dst)
}

// isSafeSkillName rejects path-traversal / separator / empty names so
// LLM-derived or user-supplied identifiers can't escape skillDir via
// ../, absolute paths, or platform separators.
func isSafeSkillName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.ContainsAny(name, `/\`) {
		return false
	}
	if strings.Contains(name, "..") {
		return false
	}
	return filepath.Base(name) == name
}
