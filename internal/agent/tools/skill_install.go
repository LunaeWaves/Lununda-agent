package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/skills"
)

// RegisterSkillInstall wires the per-agent skill search and install tools
// into registry r.
//
// agentSkillsDir is the per-agent skills directory (conventionally
// <agentHome>/skills). Agent-initiated installs always land under that path —
// never in the global ~/.fastclaw/skills/ — so one agent can't alter another
// agent's capabilities just by chatting.
//
// onReload is called after a successful install so the owning agent can
// re-scan its skills dir and expose the new skill on the next turn without
// a restart. Pass nil to disable hot reload.
func RegisterSkillInstall(r *Registry, agentSkillsDir string, onReload func()) {
	r.Register(
		"search_skills",
		"Search for skills on skills.sh (primary registry) and clawhub.ai. Returns the top matches so you can pick one to install.",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{
					"type":        "string",
					"description": "Search query (e.g. 'pdf', 'translation', 'web scraping')",
				},
			},
			"required": []string{"query"},
		},
		func(ctx context.Context, args json.RawMessage) (string, error) {
			var params struct {
				Query string `json:"query"`
			}
			if err := json.Unmarshal(args, &params); err != nil {
				return "", err
			}
			if params.Query == "" {
				return "", fmt.Errorf("query is required")
			}

			var b strings.Builder
			sh, shErr := skills.SearchSkillsSh(params.Query)
			if shErr == nil && len(sh) > 0 {
				fmt.Fprintf(&b, "skills.sh (%d results):\n", len(sh))
				limit := 10
				if len(sh) < limit {
					limit = len(sh)
				}
				for _, r := range sh[:limit] {
					fmt.Fprintf(&b, "- %s (from %s, %d installs)\n", r.SkillID, r.Source, r.Installs)
				}
			} else if shErr != nil {
				fmt.Fprintf(&b, "skills.sh search error: %v\n", shErr)
			} else {
				b.WriteString("skills.sh: no matches\n")
			}
			return b.String(), nil
		},
	)

	r.Register(
		"install_skill",
		"Install a skill into THIS agent's private skills directory. The `source` argument accepts anything the user gives you verbatim:\n"+
			"- a GitHub URL: https://github.com/owner/repo (optionally behind a mirror prefix like https://ghfast.top/https://github.com/...)\n"+
			"- a GitHub 'owner/repo' shorthand\n"+
			"- a skills.sh / clawhub.ai slug\n"+
			"You don't need to parse or transform the input — pass it through as-is. Installed skills are scoped to this agent only; they do not affect other agents.",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"source": map[string]interface{}{
					"type":        "string",
					"description": "The skill source: a GitHub URL, owner/repo, or skills.sh/clawhub slug. Pass the user's input verbatim.",
				},
			},
			"required": []string{"source"},
		},
		func(ctx context.Context, args json.RawMessage) (string, error) {
			var params struct {
				Source string `json:"source"`
			}
			if err := json.Unmarshal(args, &params); err != nil {
				return "", err
			}
			if params.Source == "" {
				return "", fmt.Errorf("source is required")
			}
			if agentSkillsDir == "" {
				return "", fmt.Errorf("agent skills directory not configured")
			}

			var (
				result *skills.Result
				err    error
			)
			if looksLikeGitHubSource(params.Source) {
				// Empty skillName = whole-repo install. The vast majority of
				// "install this GitHub repo" requests are repo-as-skill, and
				// findSkillDirInTarball would 404 on those. Nested skills are
				// rare and covered by skills.sh installs instead.
				result, err = skills.InstallFromGitHubRepo(params.Source, "", agentSkillsDir)
			} else {
				result, err = skills.InstallAuto(params.Source, agentSkillsDir)
			}
			if err != nil {
				return "", fmt.Errorf("%w — if the user still wants this capability, offer to build a custom skill using the skill-creator skill", err)
			}

			if onReload != nil {
				onReload()
			}
			msg := fmt.Sprintf("Installed %q from %s to %s (%d files).", result.Name, result.Source, result.InstalledAt, result.FilesWritten)
			if result.Version != "" {
				msg += fmt.Sprintf(" Version/ref: %s.", result.Version)
			}
			msg += " The skill is now available in this agent only; it will be picked up on the next turn."
			return msg, nil
		},
	)
}

// looksLikeGitHubSource reports whether source refers to a GitHub repo:
// a github.com URL (possibly behind a mirror like ghfast.top), or an
// "owner/repo" shorthand (exactly one slash, no scheme). Anything else
// is treated as a skills.sh / clawhub slug.
func looksLikeGitHubSource(source string) bool {
	s := strings.TrimSpace(source)
	if s == "" {
		return false
	}
	if strings.Contains(s, "github.com") {
		return true
	}
	// owner/repo shorthand: exactly one '/', no scheme, no spaces.
	if strings.Contains(s, "://") {
		return false
	}
	if strings.Count(s, "/") == 1 && !strings.ContainsAny(s, " \t") {
		return true
	}
	return false
}
