package skills

import (
	"fmt"
	"os"
	"strings"
)

// InstallFromGitHubRepo installs a skill folder from a public GitHub repo
// identified by "owner/repo". If skillName is empty, the repo itself is
// assumed to be the skill (tarball root is extracted into
// targetDir/<repo>/). Otherwise it looks up the skill folder (at any depth)
// inside the repo and extracts it to targetDir/<skillName>/.
func InstallFromGitHubRepo(repo, skillName, targetDir string) (*Result, error) {
	repo = normalizeGitHubRepo(repo)
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("repo must be owner/repo, got %q", repo)
	}
	owner, name := parts[0], parts[1]

	client := defaultHTTPClient()
	// FASTCLAW_GH_PROXY optionally mirrors github.com archive tarballs
	// through an accelerator prefix (e.g. "https://ghfast.top/"), so
	// installs succeed from networks where GitHub is blocked/throttled.
	ghMirror := strings.TrimRight(os.Getenv("FASTCLAW_GH_PROXY"), "/")
	var lastErr error
	for _, ref := range []string{"main", "master"} {
		// github.com archive, not codeload: identical tarball, but mirrors
		// like ghfast.top proxy github.com and reject codeload.github.com.
		tarURL := fmt.Sprintf("https://github.com/%s/%s/archive/refs/heads/%s.tar.gz", owner, name, ref)
		if ghMirror != "" {
			tarURL = ghMirror + "/" + tarURL
		}

		subpath := ""
		dest := ""
		installedName := skillName

		// Whole-repo skill when no skillName is given, OR when skillName
		// equals the repo name — the latter is the common "install this
		// GitHub repo as a skill" case (LLM passes name=repo, e.g.
		// name=huashu-design repo=alchaincyf/huashu-design). The skill
		// lives at the repo root, not in a <skillName>/ subfolder, so
		// findSkillDirInTarball would miss it and 404.
		if skillName == "" || skillName == name {
			// Whole-repo skill: extract the tarball top into targetDir/<name>.
			installedName = name
			dest = fmt.Sprintf("%s/%s", strings.TrimRight(targetDir, "/"), installedName)
		} else {
			found, err := findSkillDirInTarball(client, tarURL, skillName)
			if err != nil {
				lastErr = err
				continue
			}
			if found == "" {
				lastErr = fmt.Errorf("skill %q not found in %s/%s@%s", skillName, owner, name, ref)
				continue
			}
			subpath = found
			dest = fmt.Sprintf("%s/%s", strings.TrimRight(targetDir, "/"), skillName)
		}

		n, err := extractSubpath(client, tarURL, subpath, dest)
		if err != nil {
			lastErr = err
			continue
		}
		if n == 0 {
			lastErr = fmt.Errorf("extracted no files from %s", tarURL)
			continue
		}
		return &Result{
			Source:       "github",
			Name:         installedName,
			Version:      ref,
			InstalledAt:  dest,
			FilesWritten: n,
		}, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no main or master branch on %s", repo)
	}
	return nil, lastErr
}

// normalizeGitHubRepo strips common wrapper prefixes/suffixes so callers can
// pass things like "https://github.com/owner/repo.git" directly, including
// when wrapped behind a mirror like "https://ghfast.top/https://github.com/...".
func normalizeGitHubRepo(repo string) string {
	// Unwrap mirror prefixes: anything before an embedded github.com URL.
	// e.g. "https://ghfast.top/https://github.com/owner/repo" -> "https://github.com/owner/repo"
	for _, marker := range []string{"https://github.com/", "http://github.com/", "github.com/"} {
		if idx := strings.Index(repo, marker); idx > 0 {
			repo = repo[idx:]
			break
		}
	}
	repo = strings.TrimPrefix(repo, "https://github.com/")
	repo = strings.TrimPrefix(repo, "http://github.com/")
	repo = strings.TrimPrefix(repo, "github.com/")
	repo = strings.TrimSuffix(repo, ".git")
	repo = strings.Trim(repo, "/")
	return repo
}
