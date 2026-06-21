package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestApplyEdit pins the contract that edit_file's three backends share:
// a single match replaces in place, the empty / equal / not-found / multi-
// match cases each error with a fragment the LLM can act on, and
// replace_all swaps every occurrence. Pure-function tests only — backend
// routing is exercised through the running agent.
func TestApplyEdit(t *testing.T) {
	const (
		path  = "MEMORY.md"
		oldS  = "alpha"
		newS  = "beta"
		multi = "alpha and alpha"
	)

	cases := []struct {
		name       string
		content    string
		oldStr     string
		newStr     string
		replaceAll bool

		wantContent string
		wantCount   int
		wantErrSub  string // substring; empty == expect no error
	}{
		{
			name:        "single match replaces in place",
			content:     "x alpha y",
			oldStr:      oldS,
			newStr:      newS,
			wantContent: "x beta y",
			wantCount:   1,
		},
		{
			name:        "replace_all swaps every occurrence",
			content:     multi,
			oldStr:      oldS,
			newStr:      newS,
			replaceAll:  true,
			wantContent: "beta and beta",
			wantCount:   2,
		},
		{
			name:       "multi match without replace_all errors with count and hint",
			content:    multi,
			oldStr:     oldS,
			newStr:     newS,
			wantErrSub: "matches 2 locations",
		},
		{
			name:       "not found errors with path so the LLM knows which file to re-read",
			content:    "nothing here",
			oldStr:     oldS,
			newStr:     newS,
			wantErrSub: "not found in " + path,
		},
		{
			name:       "empty old_string rejected (use write_file instead)",
			content:    "anything",
			oldStr:     "",
			newStr:     newS,
			wantErrSub: "old_string is empty",
		},
		{
			name:       "no-op edit (old == new) rejected",
			content:    "x alpha y",
			oldStr:     oldS,
			newStr:     oldS,
			wantErrSub: "must differ",
		},
		{
			name:        "replace_all with single match still works",
			content:     "x alpha y",
			oldStr:      oldS,
			newStr:      newS,
			replaceAll:  true,
			wantContent: "x beta y",
			wantCount:   1,
		},
		{
			name:        "whitespace-sensitive match (indentation matters)",
			content:     "  alpha\n",
			oldStr:      "  alpha",
			newStr:      "  beta",
			wantContent: "  beta\n",
			wantCount:   1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, count, err := applyEdit(path, tc.content, tc.oldStr, tc.newStr, tc.replaceAll)

			if tc.wantErrSub != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil (content=%q)", tc.wantErrSub, got)
				}
				if !strings.Contains(err.Error(), tc.wantErrSub) {
					t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErrSub)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.wantContent {
				t.Errorf("content mismatch:\n  got:  %q\n  want: %q", got, tc.wantContent)
			}
			if count != tc.wantCount {
				t.Errorf("count mismatch: got %d, want %d", count, tc.wantCount)
			}
		})
	}
}

// TestValidateFileTargetPath_WindowsReservedNames pins Bug 2: paths whose
// any segment stem matches a DOS device name (nul/con/aux/prn/comN/lptN)
// are rejected so Windows doesn't materialize an unopenable phantom file.
func TestValidateFileTargetPath_WindowsReservedNames(t *testing.T) {
	bad := []string{"nul", "NUL", "nul.txt", "nul.tar.gz", "dir/con", "COM1.log", "a/b/AUX", "lpt9"}
	for _, p := range bad {
		if err := validateFileTargetPath(p); err == nil {
			t.Errorf("expected rejection for reserved-name path %q, got nil", p)
		}
	}
	ok := []string{"null", "nulles.txt", "console.log", "report.nul.tar.gz", "report.md", "sub/dir/file.txt", "COM10.txt"}
	for _, p := range ok {
		if err := validateFileTargetPath(p); err != nil {
			t.Errorf("expected acceptance for %q, got %v", p, err)
		}
	}
}

// TestWorkspaceRelative pins Bug 1: an absolute path inside the agent's
// workspace dir collapses to a workspace-relative path (so writes
// session-scope under sessions/<sid>/ instead of landing at the workspace
// root). Paths outside the workspace and already-relative paths are
// returned unchanged.
func TestWorkspaceRelative(t *testing.T) {
	root := filepath.Join(os.TempDir(), "lununda-ws-test")
	r := NewRegistry("", root)
	cases := []struct{ in, want string }{
		{filepath.Join(root, "foo.html"), "foo.html"},
		{filepath.Join(root, "sub", "bar.md"), "sub/bar.md"},
		{filepath.Join(root, "a", "b", "c.txt"), "a/b/c.txt"},
		{filepath.Join(os.TempDir(), "elsewhere", "x.md"), filepath.Join(os.TempDir(), "elsewhere", "x.md")}, // outside → unchanged
		{"foo.html", "foo.html"}, // relative → unchanged
	}
	for _, c := range cases {
		if got := r.workspaceRelative(c.in); got != c.want {
			t.Errorf("workspaceRelative(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
