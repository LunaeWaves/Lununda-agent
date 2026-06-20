package agent

import (
	"strings"
	"testing"

	"github.com/LunaeWaves/Lununda-agent/internal/config"
)

func TestReviewFiresGate(t *testing.T) {
	cfg := config.ReviewCfg{Enabled: true, EveryNTurns: 10}
	cases := []struct {
		turns int
		n     int
		want  bool
	}{
		{0, 10, false},
		{10, 10, true},
		{20, 10, true},
		{15, 10, false},
	}
	for _, c := range cases {
		if got := reviewFires(cfg, c.turns, c.n); got != c.want {
			t.Errorf("turns=%d n=%d fires=%v want %v", c.turns, c.n, got, c.want)
		}
	}
	cfg.Enabled = false
	if reviewFires(cfg, 10, 10) {
		t.Error("disabled review must not fire")
	}
}

func TestReviewPromptNegativeList(t *testing.T) {
	p := buildReviewPrompt()
	for _, kw := range []string{"USER.md", "MEMORY.md", "skills/", "command not found", "SOUL.md"} {
		if !strings.Contains(p, kw) {
			t.Errorf("review prompt missing key concept %q", kw)
		}
	}
}
