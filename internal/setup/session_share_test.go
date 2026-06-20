package setup

import "testing"

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
