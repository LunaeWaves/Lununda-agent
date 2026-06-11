package kb

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// LLMCaller is a minimal interface for calling the LLM from the KB processor.
type LLMCaller interface {
	CallLLM(ctx context.Context, prompt string) (string, error)
}

// ProcessedResult holds the LLM-enhanced content for an ingested source.
type ProcessedResult struct {
	Summary string   `json:"summary"`
	Tags    []string `json:"tags"`
}

// ProcessContent calls the LLM to generate a structured summary and tags
// from the ingested text content. Returns a ProcessedResult with the
// enhanced content, or nil if LLM processing is unavailable.
func ProcessContent(ctx context.Context, llm LLMCaller, title, content string) (*ProcessedResult, error) {
	if llm == nil || content == "" {
		return nil, nil
	}

	// Truncate content to avoid excessive token usage
	src := content
	if len(src) > 4000 {
		src = src[:3800] + "\n...(truncated)"
	}

	prompt := fmt.Sprintf(`Analyze the following text and produce a JSON object with two fields:
1. "summary": A concise but comprehensive summary in the same language as the source text. Cover the key points, entities, and relationships. 200-500 characters.
2. "tags": An array of 3-8 relevant keyword tags for categorization.

Source title: %s

Source content:
%s

Respond with ONLY the JSON object, no markdown fences or explanation.`, title, src)

	resp, err := llm.CallLLM(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("kb llm process: %w", err)
	}

	// Clean up response - strip markdown fences if present
	resp = strings.TrimSpace(resp)
	resp = strings.TrimPrefix(resp, "```json")
	resp = strings.TrimPrefix(resp, "```")
	resp = strings.TrimSuffix(resp, "```")
	resp = strings.TrimSpace(resp)

	var result ProcessedResult
	if err := json.Unmarshal([]byte(resp), &result); err != nil {
		// If JSON parsing fails, use the raw response as summary
		return &ProcessedResult{
			Summary: resp,
			Tags:    []string{},
		}, nil
	}

	if result.Tags == nil {
		result.Tags = []string{}
	}

	return &result, nil
}
