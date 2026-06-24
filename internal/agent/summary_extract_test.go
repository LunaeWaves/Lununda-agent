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

func TestExtractConversationTopics_HappyPath(t *testing.T) {
	mp := &mockSummaryProvider{
		response: `{"topics":[{"topic":"auth bug fix","summary":"We fixed the bug","keywords":["bug","fix"],"importance":4,"segments":[{"s":100,"e":102}]}]}`,
	}

	msgs := []provider.Message{
		{Role: "user", Content: "Hey there's a bug in the auth flow"},
		{Role: "assistant", Content: "Let me look. Found it — line 42"},
		{Role: "user", Content: "Great, fixed?"},
	}

	topics, err := extractConversationTopics(context.Background(), mp, "mock-model", msgs, 100, 200)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(topics) != 1 {
		t.Fatalf("expected 1 topic, got %d (%+v)", len(topics), topics)
	}
	if topics[0].Summary != "We fixed the bug" {
		t.Errorf("summary: %q", topics[0].Summary)
	}
	if topics[0].Topic != "auth bug fix" {
		t.Errorf("topic: %q", topics[0].Topic)
	}
	if len(topics[0].Keywords) != 2 {
		t.Errorf("keywords: %v", topics[0].Keywords)
	}
	if len(topics[0].Segments) != 1 || topics[0].Segments[0].S != 100 || topics[0].Segments[0].E != 102 {
		t.Errorf("segments: %+v", topics[0].Segments)
	}
	if mp.called != 1 {
		t.Errorf("expected 1 LLM call, got %d", mp.called)
	}
}

func TestExtractConversationTopics_MultipleTopics(t *testing.T) {
	mp := &mockSummaryProvider{
		response: `{"topics":[
			{"topic":"weather","summary":"Discussed the weather","keywords":["rain"],"importance":1,"segments":[{"s":1,"e":2}]},
			{"topic":"health","summary":"Blood pressure management","keywords":["bp","diet"],"importance":4,"segments":[{"s":4,"e":5},{"s":8,"e":9}]}
		]}`,
	}
	msgs := []provider.Message{
		{Role: "user", Content: "weather?"},
		{Role: "assistant", Content: "raining"},
		{Role: "user", Content: "skip"},
		{Role: "user", Content: "blood pressure?"},
		{Role: "assistant", Content: "advice"},
		{Role: "user", Content: "skip"},
		{Role: "user", Content: "skip"},
		{Role: "user", Content: "diet?"},
		{Role: "assistant", Content: "advice"},
	}

	topics, err := extractConversationTopics(context.Background(), mp, "mock-model", msgs, 1, 10)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(topics) != 2 {
		t.Fatalf("expected 2 topics, got %d", len(topics))
	}
	// Second topic has two disjoint segments
	if len(topics[1].Segments) != 2 {
		t.Errorf("expected 2 segments on health topic, got %d", len(topics[1].Segments))
	}
}

func TestExtractConversationTopics_EmptyTopics(t *testing.T) {
	mp := &mockSummaryProvider{
		response: `{"topics":[]}`,
	}
	msgs := []provider.Message{
		{Role: "user", Content: "Hi"},
		{Role: "assistant", Content: "Hello"},
	}

	topics, err := extractConversationTopics(context.Background(), mp, "mock-model", msgs, 1, 2)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if topics != nil {
		t.Errorf("expected nil for empty topics, got %+v", topics)
	}
}

func TestExtractConversationTopics_JSONWithFences(t *testing.T) {
	mp := &mockSummaryProvider{
		response: "```json\n{\"topics\":[{\"topic\":\"x\",\"summary\":\"test\",\"keywords\":[\"x\"],\"importance\":3,\"segments\":[{\"s\":1,\"e\":1}]}]}\n```",
	}
	msgs := []provider.Message{{Role: "user", Content: "test"}}

	topics, err := extractConversationTopics(context.Background(), mp, "mock-model", msgs, 1, 2)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(topics) != 1 || topics[0].Summary != "test" {
		t.Errorf("expected 1 topic summary=test, got %+v", topics)
	}
}

func TestExtractConversationTopics_NoMessages(t *testing.T) {
	mp := &mockSummaryProvider{}
	topics, err := extractConversationTopics(context.Background(), mp, "mock-model", nil, 1, 2)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if topics != nil {
		t.Errorf("expected nil for empty messages, got %+v", topics)
	}
	if mp.called != 0 {
		t.Errorf("expected 0 LLM calls, got %d", mp.called)
	}
}

func TestExtractConversationTopics_SkipsSyntheticOrigin(t *testing.T) {
	// Messages with Origin set are synthetic (e.g. goal-context continuations).
	// They must NOT be transcribed — they're scaffolding, not user content.
	mp := &mockSummaryProvider{
		response: `{"topics":[]}`,
	}
	msgs := []provider.Message{
		{Role: "user", Content: "real msg"},
		{Role: "assistant", Content: "synthetic", Origin: "goal_context"},
	}

	_, err := extractConversationTopics(context.Background(), mp, "mock-model", msgs, 1, 2)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	// LLM was called with only the real msg (transcript non-empty after filtering)
	if mp.called != 1 {
		t.Errorf("expected 1 LLM call after filtering synthetic, got %d", mp.called)
	}
}

func TestExtractConversationTopics_AllSyntheticShortCircuits(t *testing.T) {
	// All messages synthetic → transcript empty → no LLM call at all.
	mp := &mockSummaryProvider{}
	msgs := []provider.Message{
		{Role: "assistant", Content: "synthetic", Origin: "goal_context"},
	}

	topics, err := extractConversationTopics(context.Background(), mp, "mock-model", msgs, 1, 2)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if topics != nil {
		t.Errorf("expected nil for all-synthetic, got %+v", topics)
	}
	if mp.called != 0 {
		t.Errorf("expected 0 LLM calls, got %d", mp.called)
	}
}

// LLM 标的 seq 段可能落在窗口外（幻觉数字）。越界的段必须丢弃；
// 一个 topic 如果所有段都越界，整个 topic 丢弃。这是不让 LLM
// 数字污染检索的关键护栏。
func TestExtractConversationTopics_DropsOutOfRangeSegments(t *testing.T) {
	mp := &mockSummaryProvider{
		response: `{"topics":[
			{"topic":"keeps-in-range","summary":"legal","keywords":["a"],"importance":3,"segments":[{"s":1,"e":3},{"s":1,"e":99}]},
			{"topic":"drops-fully-out","summary":"illegal","keywords":["b"],"importance":3,"segments":[{"s":50,"e":60}]}
		]}`,
	}
	msgs := []provider.Message{
		{Role: "user", Content: "a"},
		{Role: "assistant", Content: "b"},
		{Role: "user", Content: "c"},
		{Role: "assistant", Content: "d"},
	}

	topics, err := extractConversationTopics(context.Background(), mp, "mock-model", msgs, 1, 10)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(topics) != 1 {
		t.Fatalf("expected 1 topic (out-of-range one dropped), got %d (%+v)", len(topics), topics)
	}
	if topics[0].Topic != "keeps-in-range" {
		t.Errorf("wrong topic kept: %+v", topics[0])
	}
	// [1,99] has e=99 > 10 → dropped; only [1,3] survives
	if len(topics[0].Segments) != 1 || topics[0].Segments[0].E != 3 {
		t.Errorf("out-of-range segment not dropped: %+v", topics[0].Segments)
	}
}
