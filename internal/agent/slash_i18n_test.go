package agent

import (
	"strings"
	"testing"
)

func TestExpandSlashSentinelLocale(t *testing.T) {
	sentinel := slashReply("claim_success", map[string]any{"channel": "wechat"})

	if got := expandSlashSentinel(sentinel, "en"); !strings.Contains(got, "Claimed") {
		t.Errorf("en locale: %q want 'Claimed'", got)
	}

	zh := expandSlashSentinel(sentinel, "zh-CN")
	if !strings.Contains(zh, "认领成功") {
		t.Errorf("zh-CN locale: %q want '认领成功'", zh)
	}
	if !strings.Contains(zh, "wechat") {
		t.Errorf("zh-CN locale missing {channel} arg: %q", zh)
	}

	// empty / unknown locale → fallback to en
	if got := expandSlashSentinel(sentinel, ""); !strings.Contains(got, "Claimed") {
		t.Errorf("empty locale: %q want en fallback", got)
	}
	if got := expandSlashSentinel(sentinel, "fr"); !strings.Contains(got, "Claimed") {
		t.Errorf("unknown locale: %q want en fallback", got)
	}

	// non-sentinel content passes through unchanged
	if got := expandSlashSentinel("plain text", "zh-CN"); got != "plain text" {
		t.Errorf("non-sentinel: %q want unchanged", got)
	}
}
