package store

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestSchemaInsertConsistency walks every .go file under internal/, parses
// every `INSERT INTO <table> (col1, col2, ...)` literal, and verifies each
// referenced column exists in the migrated schema. Catches the kb_entries
// class of bug (INSERT column missing from CREATE TABLE) before runtime.
func TestSchemaInsertConsistency(t *testing.T) {
	d, err := NewDBStore("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("NewDBStore: %v", err)
	}
	defer d.Close()
	if err := d.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Pass 1: collect table names (close rows before re-querying — DBStore
	// uses a single sqlite connection, so nested queries deadlock).
	rows, err := d.db.Query("SELECT name FROM sqlite_master WHERE type='table'")
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	var tableNames []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tableNames = append(tableNames, name)
	}
	rows.Close()

	// Pass 2: per-table PRAGMA table_info -> column set.
	schemaCols := map[string]map[string]bool{}
	for _, name := range tableNames {
		info, err := d.db.Query("PRAGMA table_info(" + name + ")")
		if err != nil {
			continue
		}
		cols := map[string]bool{}
		for info.Next() {
			var cid, notnull, pk int
			var cname, ctype string
			var dflt any
			if err := info.Scan(&cid, &cname, &ctype, &notnull, &dflt, &pk); err == nil {
				cols[strings.ToLower(cname)] = true
			}
		}
		info.Close()
		schemaCols[strings.ToLower(name)] = cols
	}

	repoRoot := findRepoRoot(t)

	insertRe := regexp.MustCompile(`(?is)INSERT\s+INTO\s+(\w+)\s*\(([^)]+)\)`)
	colSplitRe := regexp.MustCompile(`[\s,]+`)
	identRe := regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

	skipTokens := map[string]bool{
		"values": true, "select": true, "true": true, "false": true,
		"null": true, "default": true, "current_timestamp": true,
	}

	type problem struct{ file, table, col string }

	// known false positives — INSERTs in migration functions that
	// reference legacy schema columns. Guarded by tableHasColumn
	// checks, never execute on fresh DBs. Listed here so the test
	// stays a meaningful regression for new code.
	knownFalsePositive := map[string]bool{
		"configs.scope_id": true, // migrateSkillsAgentEntriesSplit / migrateAgentsDropModel
	}
	var problems []problem

	_ = filepath.Walk(filepath.Join(repoRoot, "internal"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(repoRoot, path)
		for _, m := range insertRe.FindAllStringSubmatch(string(data), -1) {
			table := strings.ToLower(m[1])
			schema, ok := schemaCols[table]
			if !ok {
				continue
			}
			for _, raw := range colSplitRe.Split(m[2], -1) {
				col := strings.ToLower(strings.TrimSpace(raw))
				if col == "" || skipTokens[col] {
					continue
				}
				if strings.ContainsAny(col, "%()?") {
					continue
				}
				if !identRe.MatchString(col) {
					continue
				}
				if knownFalsePositive[table+"."+col] {
					continue
				}
				if !schema[col] {
					problems = append(problems, problem{rel, table, col})
				}
			}
		}
		return nil
	})

	if len(problems) > 0 {
		lines := make([]string, 0, len(problems))
		for _, p := range problems {
			lines = append(lines, "  "+p.table+"."+p.col+"\t← "+p.file)
		}
		t.Errorf("schema/INSERT inconsistencies (%d):\n%s",
			len(problems), strings.Join(lines, "\n"))
	}
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("go.mod not found walking up from cwd")
	return ""
}
