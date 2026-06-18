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
