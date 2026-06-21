package agent

import (
	"testing"
	"time"

	"github.com/LunaeWaves/Lununda-agent/internal/config"
)

func TestShouldRunSkillEvolutionGating(t *testing.T) {
	week := 7 * 24 * time.Hour

	// 启用且超期 → true
	if !shouldRunSkillEvolution(config.SkillEvolutionCfg{Enabled: true, Interval: week},
		time.Now().Add(-8*24*time.Hour)) {
		t.Errorf("启用且超期应触发")
	}
	// 未启用 → false
	if shouldRunSkillEvolution(config.SkillEvolutionCfg{Enabled: false, Interval: week},
		time.Now().Add(-8*24*time.Hour)) {
		t.Errorf("未启用不应触发")
	}
	// 未到期 → false
	if shouldRunSkillEvolution(config.SkillEvolutionCfg{Enabled: true, Interval: week},
		time.Now().Add(-1*time.Hour)) {
		t.Errorf("未到期不应触发")
	}
	// 从未跑（零时间）→ false（首次延后一个周期）
	if shouldRunSkillEvolution(config.SkillEvolutionCfg{Enabled: true, Interval: week},
		time.Time{}) {
		t.Errorf("首次应延后一个周期，不立即触发")
	}
	// interval<=0 → false
	if shouldRunSkillEvolution(config.SkillEvolutionCfg{Enabled: true, Interval: 0},
		time.Now().Add(-8*24*time.Hour)) {
		t.Errorf("interval<=0 不应触发")
	}
}
