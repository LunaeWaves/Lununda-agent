package store

import (
	"context"
	"testing"
)

func setupTestDB(t *testing.T) *DBStore {
	t.Helper()
	d, err := NewDBStore("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("NewDBStore: %v", err)
	}
	if err := d.Migrate(context.Background()); err != nil {
		d.Close()
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestInsertAndSearchConversationSummaries(t *testing.T) {
	d := setupTestDB(t)
	ctx := context.Background()

	// Insert 2 summaries
	id1, err := d.InsertConversationSummary(ctx, ConversationSummary{
		UserID: "u1", AgentID: "a1", SessionKey: "s1", ChatterUserID: "c1",
		Summary: "We fixed the hardline pattern Windows gap",
		Keywords: []string{"hardline", "windows", "patterns"},
		SeqStart: 100, SeqEnd: 200,
	})
	if err != nil {
		t.Fatalf("insert 1: %v", err)
	}
	if id1 == 0 {
		t.Fatal("got id=0")
	}

	_, err = d.InsertConversationSummary(ctx, ConversationSummary{
		UserID: "u1", AgentID: "a1", SessionKey: "s1", ChatterUserID: "c1",
		Summary: "Discussed sqlite-vec integration for memory recall",
		Keywords: []string{"sqlite-vec", "memory", "embedding"},
		SeqStart: 201, SeqEnd: 300,
	})
	if err != nil {
		t.Fatalf("insert 2: %v", err)
	}

	// FTS search "hardline"
	hits, err := d.SearchConversationSummariesFTS(ctx, "c1", "a1", "hardline", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(hits))
	}
	if hits[0].Summary != "We fixed the hardline pattern Windows gap" {
		t.Errorf("unexpected summary: %q", hits[0].Summary)
	}
	if len(hits[0].Keywords) != 3 {
		t.Errorf("expected 3 keywords, got %d", len(hits[0].Keywords))
	}

	// FTS search "memory" — should match the second summary
	hits2, err := d.SearchConversationSummariesFTS(ctx, "c1", "a1", "memory", 10)
	if err != nil {
		t.Fatalf("search 2: %v", err)
	}
	if len(hits2) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(hits2))
	}
	if hits2[0].SeqStart != 201 {
		t.Errorf("wrong seq_start: %d", hits2[0].SeqStart)
	}
}

func TestConversationSummariesChatterIsolation(t *testing.T) {
	// chatter c2 must NOT see chatter c1's summaries, even if query matches.
	d := setupTestDB(t)
	ctx := context.Background()

	_, err := d.InsertConversationSummary(ctx, ConversationSummary{
		UserID: "u1", AgentID: "a1", SessionKey: "s1", ChatterUserID: "c1",
		Summary: "secret chatter1 data about project x",
		Keywords: []string{"secret"},
		SeqStart: 1, SeqEnd: 2,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// chatter c2 searching for "secret" — must get zero hits
	hits, err := d.SearchConversationSummariesFTS(ctx, "c2", "a1", "secret", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("chatter isolation violated: c2 saw %d of c1's summaries", len(hits))
	}
}

func TestConversationSummaryMetaRoundTrip(t *testing.T) {
	d := setupTestDB(t)
	ctx := context.Background()

	// Empty by default
	v, err := d.GetConversationSummaryMeta(ctx, "embedding_model_in_use")
	if err != nil {
		t.Fatalf("get empty: %v", err)
	}
	if v != "" {
		t.Errorf("expected empty, got %q", v)
	}

	// Set
	if err := d.SetConversationSummaryMeta(ctx, "embedding_model_in_use", "text-embedding-3-small"); err != nil {
		t.Fatalf("set: %v", err)
	}

	// Get
	v, err = d.GetConversationSummaryMeta(ctx, "embedding_model_in_use")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if v != "text-embedding-3-small" {
		t.Errorf("expected 'text-embedding-3-small', got %q", v)
	}

	// Upsert
	if err := d.SetConversationSummaryMeta(ctx, "embedding_model_in_use", "nomic-embed-text"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	v, _ = d.GetConversationSummaryMeta(ctx, "embedding_model_in_use")
	if v != "nomic-embed-text" {
		t.Errorf("expected nomic, got %q", v)
	}
}

// TestConversationSummaryChineseRecall verifies CJK search works.
// unicode61 tokenizer (the original FTS5 setup) can't match CJK
// substrings — "讨论" returns 0 rows against Chinese summaries.
// The current LIKE-based path fixes this.
func TestConversationSummaryChineseRecall(t *testing.T) {
	d := setupTestDB(t)
	ctx := context.Background()

	_, err := d.InsertConversationSummary(ctx, ConversationSummary{
		UserID: "u1", AgentID: "a1", SessionKey: "s1", ChatterUserID: "c1",
		Summary: "我们今天讨论了 sqlite-vec 集成方案",
		Keywords: []string{"sqlite-vec", "集成", "向量搜索"},
		SeqStart: 1, SeqEnd: 2,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// CJK substrings that unicode61 cannot match
	cases := []struct {
		query string
		want  int // expected hit count
	}{
		{"讨论", 1},   // 2-char CJK
		{"集成", 1},   // 2-char CJK in summary AND keywords
		{"向量", 1},   // 2-char CJK keyword-only
		{"今天", 1},   // 2-char CJK summary-only
		{"sqlite", 1}, // ASCII substring
		{"不存在的词", 0}, // negative
	}
	for _, c := range cases {
		hits, err := d.SearchConversationSummariesFTS(ctx, "c1", "a1", c.query, 10)
		if err != nil {
			t.Errorf("search %q: %v", c.query, err)
			continue
		}
		if len(hits) != c.want {
			t.Errorf("search %q: got %d hits, want %d", c.query, len(hits), c.want)
		}
	}
}

func TestConversationSummaryVectorRoundTrip(t *testing.T) {
	d := setupTestDB(t)
	ctx := context.Background()

	// Insert a summary
	id, err := d.InsertConversationSummary(ctx, ConversationSummary{
		UserID: "u1", AgentID: "a1", SessionKey: "s1", ChatterUserID: "c1",
		Summary: "We discussed vector search integration",
		Keywords: []string{"vector", "search"},
		SeqStart: 1, SeqEnd: 2,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Insert vector
	vec := make([]float32, 1024)
	vec[0] = 0.5
	vec[1] = 0.3
	if err := d.InsertConversationSummaryVector(ctx, id, vec); err != nil {
		t.Fatalf("insert vector: %v", err)
	}

	// KNN search with a similar vector
	queryVec := make([]float32, 1024)
	queryVec[0] = 0.5
	queryVec[1] = 0.3
	ids, err := d.SearchConversationSummariesVector(ctx, queryVec, 10)
	if err != nil {
		t.Fatalf("search vector: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("expected 1 result, got %d", len(ids))
	}
	if ids[0] != id {
		t.Errorf("expected id %d, got %d", id, ids[0])
	}
}

func TestConversationSummaryVectorNeedsList(t *testing.T) {
	d := setupTestDB(t)
	ctx := context.Background()

	// Insert 2 summaries, only 1 gets a vector
	id1, _ := d.InsertConversationSummary(ctx, ConversationSummary{
		UserID: "u1", AgentID: "a1", SessionKey: "s1", ChatterUserID: "c1",
		Summary: "summary 1", Keywords: []string{"a"},
		SeqStart: 1, SeqEnd: 2,
	})
	id2, _ := d.InsertConversationSummary(ctx, ConversationSummary{
		UserID: "u1", AgentID: "a1", SessionKey: "s1", ChatterUserID: "c1",
		Summary: "summary 2", Keywords: []string{"b"},
		SeqStart: 3, SeqEnd: 4, EmbeddingModel: "old-model",
	})

	vec := make([]float32, 1024)
	d.InsertConversationSummaryVector(ctx, id1, vec)

	// List needing vector
	needs, err := d.ListConversationSummariesNeedingVector(ctx, "", 100)
	if err != nil {
		t.Fatalf("list needing: %v", err)
	}
	if len(needs) != 1 {
		t.Fatalf("expected 1 needing vector, got %d", len(needs))
	}
	if needs[0].ID != id2 {
		t.Errorf("expected id2 needing vector, got %d", needs[0].ID)
	}

	// With model filter: id2 has old-model, should surface
	needs2, err := d.ListConversationSummariesNeedingVector(ctx, "new-model", 100)
	if err != nil {
		t.Fatalf("list needing with model: %v", err)
	}
	if len(needs2) != 2 { // both: id1 has no model set, id2 has old-model
		t.Fatalf("expected 2 needing vector with model switch, got %d", len(needs2))
	}
}

func TestConversationSummaryClearVectors(t *testing.T) {
	d := setupTestDB(t)
	ctx := context.Background()

	id, _ := d.InsertConversationSummary(ctx, ConversationSummary{
		UserID: "u1", AgentID: "a1", SessionKey: "s1", ChatterUserID: "c1",
		Summary: "test", Keywords: []string{"x"},
		SeqStart: 1, SeqEnd: 2,
	})
	vec := make([]float32, 1024)
	d.InsertConversationSummaryVector(ctx, id, vec)

	// Clear
	if err := d.ClearConversationSummaryVectors(ctx); err != nil {
		t.Fatalf("clear: %v", err)
	}

	// Verify empty
	ids, err := d.SearchConversationSummariesVector(ctx, vec, 10)
	if err != nil {
		t.Fatalf("search after clear: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("expected 0 results after clear, got %d", len(ids))
	}
}
