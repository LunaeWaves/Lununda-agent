package agent

import (
	"context"

	"github.com/LunaeWaves/Lununda-agent/internal/store"
)

// MemoryStoreAdapter exposes the agent's identity + memory files via the
// underlying store. Reads pass userID through so the per-owner override
// row wins when present (USER.md / MEMORY.md the agent autopersisted
// for its owner); writes also carry userID so chat-time updates land
// in the owner's row, never the shared template.
type MemoryStoreAdapter struct {
	st store.Store
}

func NewMemoryStoreAdapter(st store.Store) *MemoryStoreAdapter {
	return &MemoryStoreAdapter{st: st}
}

const memoryFilename = "MEMORY.md"

// GetMemory uses the *Exact* (no owner-fallback) variant deliberately.
// MEMORY.md is per-(agent, owner) — keyed by (agentID, ownerID).
func (a *MemoryStoreAdapter) GetMemory(ctx context.Context, agentID, userID string) (string, error) {
	data, err := a.st.GetAgentFileExact(ctx, agentID, userID, memoryFilename)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (a *MemoryStoreAdapter) SaveMemory(ctx context.Context, agentID, userID, content string) error {
	return a.st.SaveAgentFile(ctx, agentID, userID, memoryFilename, store.OriginForeground, []byte(content))
}

// GetWorkspaceFile keeps the owner-fallback overlay because the
// ContextBuilder uses this method for shared identity files
// (SOUL/IDENTITY/AGENTS/BOOTSTRAP/HEARTBEAT/TOOLS). Chatters inheriting
// the owner's identity is the desired behavior there.
func (a *MemoryStoreAdapter) GetWorkspaceFile(ctx context.Context, agentID, userID, filename string) ([]byte, error) {
	return a.st.GetAgentFile(ctx, agentID, userID, filename)
}

// GetWorkspaceFileExact bypasses the owner-fallback overlay. Used for
// per-chatter files (USER.md) so a fresh visitor sees an empty profile
// instead of the owner's.
func (a *MemoryStoreAdapter) GetWorkspaceFileExact(ctx context.Context, agentID, userID, filename string) ([]byte, error) {
	return a.st.GetAgentFileExact(ctx, agentID, userID, filename)
}

func (a *MemoryStoreAdapter) SaveWorkspaceFile(ctx context.Context, agentID, userID, filename, origin string, data []byte) error {
	return a.st.SaveAgentFile(ctx, agentID, userID, filename, origin, data)
}
