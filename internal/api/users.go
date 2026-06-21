package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/LunaeWaves/Lununda-agent/internal/auth"
)

// HandleProvisionAppUser handles POST /v1/users.
//
// DEPRECATED under agent privatization (D1): the app_user multi-tenant path
// was closed — SwitchToAppUser is no longer reachable from the request path.
// This endpoint is retained for backward compatibility with calling apps
// but is now a no-op: it echoes back the agent-scoped apikey owner's
// user_id and the supplied external_id unchanged. No new user is minted.
//
// Request body: { "external_id": "...", "display_name": "..." (ignored) }
// Response:     { "user_id": "<owner user_id>", "external_id": "<echo>" }
//
// Apps should migrate to per-agent apikeys (each agent's settings page
// issues its own key; the holder chats as the owner).
func (s *Server) HandleProvisionAppUser(w http.ResponseWriter, r *http.Request) {
	ident, ok := auth.FromContext(r.Context())
	if !ok || ident.AuthMethod != "apikey" || ident.APIKeyID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"error": map[string]string{"message": "api_key required", "type": "authentication_error"},
		})
		return
	}

	var req struct {
		ExternalID  string `json:"external_id"`
		DisplayName string `json:"display_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{"message": "invalid request body", "type": "invalid_request_error"},
		})
		return
	}
	req.ExternalID = strings.TrimSpace(req.ExternalID)
	if req.ExternalID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{"message": "external_id is required", "type": "invalid_request_error"},
		})
		return
	}

	// Agent privatization (D1) deprecated the app_user multi-tenant path.
	// This endpoint is kept for backward compatibility with calling apps
	// but no longer mints a new user — it returns the agent-scoped apikey
	// owner's user_id. The `external_id` field is echoed back unchanged
	// so callers reading it don't break.
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id":     ident.UserID,
		"external_id": req.ExternalID,
	})
}
