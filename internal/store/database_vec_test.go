package store

import (
	"context"
	"testing"
)

// TestSqliteVecLoaded verifies the sqlite-vec extension auto-registered.
func TestSqliteVecLoaded(t *testing.T) {
	d, err := NewDBStore("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("NewDBStore: %v", err)
	}
	defer d.Close()

	var ver string
	err = d.db.QueryRow("SELECT vec_version()").Scan(&ver)
	if err != nil {
		t.Fatalf("vec_version() failed: %v", err)
	}
	if ver == "" {
		t.Fatal("vec_version() returned empty string")
	}
	t.Logf("sqlite-vec version: %s", ver)
}

// TestSqliteVecCreateVec0Table verifies the vec0 virtual table module works.
func TestSqliteVecCreateVec0Table(t *testing.T) {
	d, err := NewDBStore("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	ctx := context.Background()
	_, err = d.db.ExecContext(ctx, `
		CREATE VIRTUAL TABLE test_vec USING vec0(
			id INTEGER PRIMARY KEY,
			embedding float[4]
		)
	`)
	if err != nil {
		t.Fatalf("create vec0 table: %v", err)
	}

	// Insert a vector
	_, err = d.db.ExecContext(ctx, `
		INSERT INTO test_vec (id, embedding) VALUES (1, vec_f32('[0.1, 0.2, 0.3, 0.4]'))
	`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// KNN search
	rows, err := d.db.QueryContext(ctx, `
		SELECT id, vec_distance_L2(embedding, vec_f32('[0.1, 0.2, 0.3, 0.4]')) AS d
		FROM test_vec
		ORDER BY d
		LIMIT 1
	`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	if !rows.Next() {
		t.Fatal("no rows returned")
	}
	var id int
	var dist float64
	if err := rows.Scan(&id, &dist); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if id != 1 {
		t.Errorf("expected id=1, got %d", id)
	}
	if dist > 0.0001 {
		t.Errorf("expected ~0 distance, got %f", dist)
	}
}
