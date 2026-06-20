package setup

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LunaeWaves/Lununda-agent/internal/store"
)

// These tests assert the contract of the session-share owner API
// (handleCreateSessionShare / handleRevokeSessionShare). They are skipped
// because exercising them end-to-end requires a fully-wired Server with a
// userResolver + UserSpace (resolveAgent returns nil without it), which the
// light newAuthTestServer harness in this package does not provide. The
// contract they encode is:
//
//   - POST /api/agents/{id}/sessions/{key}/share
//       * agent owner  -> 201 {token, url=="/share/"+token}
//       * non-owner    -> 403 "not your agent"
//       * read-only ident -> 403
//   - DELETE /api/agents/{id}/sessions/{key}/share
//       * agent owner  -> 200 {ok:true}; the active share row's RevokedAt
//                         becomes non-zero
//       * non-owner    -> 403
//
// The owner gate is delegated to s.requireAgentOwner (the canonical owner
// check used across internal/setup), and the store layer is covered by
// internal/store/session_shares_test.go (Task 1).

// TestCreateSessionShareOwnerOnly: owner POST → 201 + {token,url}; non-owner
// POST → 403; read-only → 403.
func TestCreateSessionShareOwnerOnly(t *testing.T) {
	t.Skip("requires setup Server with userResolver + UserSpace harness; " +
		"assert owner 201/non-owner 403, url == /share/{token}")
}

// TestRevokeSessionShare: owner DELETE → 200; the revoked token's
// GET /share/{token} (Task 4) → 404; the active share row's RevokedAt
// becomes non-zero.
func TestRevokeSessionShare(t *testing.T) {
	t.Skip("requires harness; assert revoke flips active share RevokedAt " +
		"to non-zero and owner DELETE returns 200")
}

// TestViewSharedSession drives handleViewSharedSession directly against a
// store-only Server (the handler touches only s.dataStore + r.PathValue,
// so no resolver/UserSpace is needed). Covers the four contract points:
//
//   - active token  -> 200 + HTML containing the message text
//   - revoked token -> 404
//   - unknown token -> 404
//   - attacker-controlled message content (<script>) is HTML-escaped
//     (&lt;script&gt;) on the public page — never executed
func TestViewSharedSession(t *testing.T) {
	ctx := context.Background()

	dbPath := t.TempDir() + "\\lununda.db"
	st, err := store.NewDBStore("sqlite", "file:"+dbPath+"?cache=shared")
	if err != nil {
		t.Fatalf("NewDBStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	const (
		ownerID    = "owner-1"
		agentID    = "agent-1"
		sessionKey = "sess-1"
	)
	tok, err := st.CreateSessionShare(ctx, agentID, sessionKey, ownerID)
	if err != nil {
		t.Fatalf("CreateSessionShare: %v", err)
	}
	xssPayload := "<script>alert(1)</script>"
	if err := st.AppendSessionMessage(ctx, ownerID, agentID, sessionKey, store.SessionMessage{
		Role:    "user",
		Content: "hello world",
	}); err != nil {
		t.Fatalf("AppendSessionMessage(user): %v", err)
	}
	if err := st.AppendSessionMessage(ctx, ownerID, agentID, sessionKey, store.SessionMessage{
		Role:    "assistant",
		Content: xssPayload,
	}); err != nil {
		t.Fatalf("AppendSessionMessage(assistant): %v", err)
	}

	s := NewServer(0)
	s.SetStore(st)

	doRequest := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/share/"+token, nil)
		req.SetPathValue("token", token)
		rr := httptest.NewRecorder()
		s.handleViewSharedSession(rr, req)
		return rr
	}

	// Active token -> 200 + both messages, with the XSS payload escaped.
	rr := doRequest(tok)
	if rr.Code != http.StatusOK {
		t.Fatalf("active token: status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "hello world") {
		t.Errorf("active token: body missing the user message text; body=%q", body)
	}
	if !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Errorf("active token: XSS payload not escaped in body; body=%q", body)
	}
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Errorf("active token: unescaped <script> leaked into body; body=%q", body)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("active token: Content-Type = %q, want text/html", ct)
	}

	// Revoked token -> 404 (existence must not leak).
	if err := st.RevokeSessionShare(ctx, tok); err != nil {
		t.Fatalf("RevokeSessionShare: %v", err)
	}
	if rr := doRequest(tok); rr.Code != http.StatusNotFound {
		t.Errorf("revoked token: status = %d, want %d", rr.Code, http.StatusNotFound)
	}

	// Unknown token -> 404.
	if rr := doRequest("does-not-exist"); rr.Code != http.StatusNotFound {
		t.Errorf("unknown token: status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

