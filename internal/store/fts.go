package store

import "time"

// FTSResult is a single full-text search hit.
type FTSResult struct {
	Content   string
	Timestamp time.Time
	AgentID   string
	ChatID    string
	Snippet   string // FTS5 snippet() function output
	Rank      float64
}
