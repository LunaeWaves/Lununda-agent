package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/LunaeWaves/Lununda-agent/internal/bus"
	"github.com/LunaeWaves/Lununda-agent/internal/config"
)

// TestRunWikiAutoGenCycleRespectsEnabledAndInterval proves the cycle's
// gating: a disabled agent never gets last_run Set, and an enabled agent
// whose Interval has not elapsed keeps its previous last_run. Both are
// negative paths — no provider/LLM is reached, so the test needs no mock.
func TestRunWikiAutoGenCycleRespectsEnabledAndInterval(t *testing.T) {
	st := newStaleTestStore(t)
	ctx := context.Background()
	g := &Gateway{store: st, bus: bus.New()}

	createTestAgent(t, st, "u-1", "agent-disabled")
	saveAgentMem(t, st, "u-1", "agent-disabled", config.MemoryCfg{
		WikiAutoGen: config.WikiAutoGenCfg{Enabled: false, Interval: time.Second},
	})

	createTestAgent(t, st, "u-2", "agent-not-due")
	saveAgentMem(t, st, "u-2", "agent-not-due", config.MemoryCfg{
		WikiAutoGen: config.WikiAutoGenCfg{Enabled: true, Interval: time.Hour},
	})
	if err := st.SetWikiAutoGenLastRun(ctx, "agent-not-due", time.Now()); err != nil {
		t.Fatalf("SetWikiAutoGenLastRun: %v", err)
	}

	runWikiAutoGenCycleForTest(ctx, g)

	gotDisabled, _ := st.GetWikiAutoGenLastRun(ctx, "agent-disabled")
	if !gotDisabled.IsZero() {
		t.Errorf("disabled agent 不应被 Set last_run，got %v", gotDisabled)
	}

	gotNotDue, _ := st.GetWikiAutoGenLastRun(ctx, "agent-not-due")
	if time.Since(gotNotDue) > 5*time.Second {
		t.Errorf("未到期 agent 不应被重新 Set last_run，got since=%v", time.Since(gotNotDue))
	}
}
