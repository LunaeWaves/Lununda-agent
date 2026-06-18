package agent

import (
	"context"
	"testing"

	"github.com/LunaeWaves/Lununda-agent/internal/provider"
)

type mockSummaryProvider struct {
	response string
	err      error
	called   int
}

func (m *mockSummaryProvider) Chat(
	_ context.Context,
	_ []provider.Message,
	_ []provider.Tool,
	_ string,
	_ int,
	_ float64,
) (*provider.Response, error) {
	m.called++
	if m.err != nil {
		return nil, m.err
	}
	return &provider.Response{Content: m.response}, nil
}

func (m *mockSummaryProvider) ChatStream(
	_ context.Context,
	_ []provider.Message,
	_ []provider.Tool,
	_ string,
	_ int,
	_ float64,
) (*provider.StreamReader, error) {
	return nil, nil
}

func TestExtractConversationSummary_HappyPath(t *testing.T) {
	mp := &mockSummaryProvider{
		response: `{"summary":"We fixed the bug","keywords":["bug","fix"],"seq_start":0,"seq_end":0}`,
	}

	msgs := []provider.Message{
		{Role: "user", Content: "Hey there's a bug in the auth flow"},
		{Role: "assistant", Content: "Let me look. Found it — line 42"},
		{Role: "user", Content: "Great, fixed?"},
	}

	ex, err := extractConversationSummary(context.Background(), mp, "mock-model", msgs, 100, 200)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if ex == nil {
		t.Fatal("expected non-nil extraction")
	}
	if ex.Summary != "We fixed the bug" {
		t.Errorf("summary: %q", ex.Summary)
	}
	if ex.SeqStart != 100 || ex.SeqEnd != 200 {
		t.Errorf("seq range: %d-%d (LLM should not be trusted)", ex.SeqStart, ex.SeqEnd)
	}
	if len(ex.Keywords) != 2 {
		t.Errorf("keywords: %v", ex.Keywords)
	}
	if mp.called != 1 {
		t.Errorf("expected 1 LLM call, got %d", mp.called)
	}
}

func TestExtractConversationSummary_EmptySummary(t *testing.T) {
	mp := &mockSummaryProvider{
		response: `{"summary":"","keywords":[],"seq_start":0,"seq_end":0}`,
	}
	msgs := []provider.Message{
		{Role: "user", Content: "Hi"},
		{Role: "assistant", Content: "Hello"},
	}

	ex, err := extractConversationSummary(context.Background(), mp, "mock-model", msgs, 1, 2)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if ex != nil {
		t.Errorf("expected nil for empty summary, got %+v", ex)
	}
}

func TestExtractConversationSummary_JSONWithFences(t *testing.T) {
	mp := &mockSummaryProvider{
		response: "```json\n{\"summary\":\"test\",\"keywords\":[\"x\"]}\n```",
	}
	msgs := []provider.Message{{Role: "user", Content: "test"}}

	ex, err := extractConversationSummary(context.Background(), mp, "mock-model", msgs, 1, 2)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if ex == nil || ex.Summary != "test" {
		t.Errorf("expected summary=test, got %+v", ex)
	}
}

func TestExtractConversationSummary_NoMessages(t *testing.T) {
	mp := &mockSummaryProvider{}
	ex, err := extractConversationSummary(context.Background(), mp, "mock-model", nil, 1, 2)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if ex != nil {
		t.Errorf("expected nil for empty messages, got %+v", ex)
	}
	if mp.called != 0 {
		t.Errorf("expected 0 LLM calls, got %d", mp.called)
	}
}

func TestExtractConversationSummary_SkipsSyntheticOrigin(t *testing.T) {
	// Messages with Origin set are synthetic (e.g. goal-context continuations).
	// They must NOT be transcribed — they're scaffolding, not user content.
	mp := &mockSummaryProvider{
		response: `{"summary":"","keywords":[],"seq_start":0,"seq_end":0}`,
	}
	msgs := []provider.Message{
		{Role: "user", Content: "real msg"},
		{Role: "assistant", Content: "synthetic", Origin: "goal_context"},
	}

	_, err := extractConversationSummary(context.Background(), mp, "mock-model", msgs, 1, 2)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	// LLM was called with only the real msg (transcript non-empty after filtering)
	if mp.called != 1 {
		t.Errorf("expected 1 LLM call after filtering synthetic, got %d", mp.called)
	}
}

func TestExtractConversationSummary_AllSyntheticShortCircuits(t *testing.T) {
	// All messages synthetic → transcript empty → no LLM call at all.
	mp := &mockSummaryProvider{}
	msgs := []provider.Message{
		{Role: "assistant", Content: "synthetic", Origin: "goal_context"},
	}

	ex, err := extractConversationSummary(context.Background(), mp, "mock-model", msgs, 1, 2)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if ex != nil {
		t.Errorf("expected nil for all-synthetic, got %+v", ex)
	}
	if mp.called != 0 {
		t.Errorf("expected 0 LLM calls, got %d", mp.called)
	}
}
