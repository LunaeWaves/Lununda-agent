package kb

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	cacheKeyPrefix = "kb:wiki:tokens:"
	cacheTTL       = 30 * time.Minute
)

// WikiCache provides Redis-backed caching of pre-computed wiki page tokens.
type WikiCache struct {
	rdb *redis.Client
}

// NewWikiCache creates a new cache. Returns nil if rdb is nil.
func NewWikiCache(rdb *redis.Client) *WikiCache {
	if rdb == nil {
		return nil
	}
	return &WikiCache{rdb: rdb}
}

// agentKey returns the Redis hash key for an agent's token cache.
func agentKey(agentID string) string {
	return cacheKeyPrefix + agentID
}

// LoadAll loads all pre-computed tokens for an agent from Redis.
// Returns nil if cache miss or error.
func (c *WikiCache) LoadAll(ctx context.Context, agentID string) map[string]*pageTokens {
	if c == nil {
		return nil
	}
	key := agentKey(agentID)
	result, err := c.rdb.HGetAll(ctx, key).Result()
	if err != nil {
		slog.Debug("wiki cache load failed", "agent", agentID, "err", err)
		return nil
	}
	if len(result) == 0 {
		return nil
	}

	pages := make(map[string]*pageTokens, len(result))
	for pageID, raw := range result {
		var pt pageTokens
		if err := json.Unmarshal([]byte(raw), &pt); err != nil {
			continue
		}
		pages[pageID] = &pt
	}
	return pages
}

// StoreAll persists pre-computed tokens for all pages of an agent.
func (c *WikiCache) StoreAll(ctx context.Context, agentID string, pages []*pageTokens) error {
	if c == nil {
		return nil
	}
	key := agentKey(agentID)
	pipe := c.rdb.Pipeline()
	pipe.Del(ctx, key)

	for _, pt := range pages {
		data, err := json.Marshal(pt)
		if err != nil {
			continue
		}
		pipe.HSet(ctx, key, pt.ID, data)
	}
	pipe.Expire(ctx, key, cacheTTL)

	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("wiki cache store: %w", err)
	}
	slog.Debug("wiki cache stored", "agent", agentID, "pages", len(pages))
	return nil
}

// Invalidate removes cached tokens for an agent.
func (c *WikiCache) Invalidate(ctx context.Context, agentID string) {
	if c == nil {
		return
	}
	c.rdb.Del(ctx, agentKey(agentID))
}

// SearchCached does full in-memory scoring using cached tokens.
func (c *WikiCache) SearchCached(ctx context.Context, agentID, query string, topK int) []scoredPage {
	cached := c.LoadAll(ctx, agentID)
	if cached == nil {
		return nil
	}
	return scoreCachedPages(cached, query, topK)
}

func scoreCachedPages(pages map[string]*pageTokens, query string, topK int) []scoredPage {
	qTokens := tokenizeSet(query)
	if len(qTokens) == 0 {
		return nil
	}

	var results []scoredPage
	for _, pt := range pages {
		score := scoreFromTokens(pt, qTokens)
		if score < 0.5 {
			continue
		}
		results = append(results, scoredPage{
			ID:      pt.ID,
			Title:   pt.TitleText,
			Summary: pt.SummaryText,
			Body:    pt.Body,
			Score:   score,
		})
	}

	sortByScore(results)
	if len(results) > topK {
		results = results[:topK]
	}
	return results
}
