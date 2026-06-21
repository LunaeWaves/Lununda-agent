package setup

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/LunaeWaves/Lununda-agent/internal/agent"
)

func (s *Server) handleListSkillProposals(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if s.requireAgentOwner(w, r, agentID) == nil {
		return
	}
	pending, err := s.dataStore.ListPendingProposals(r.Context(), agentID)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "proposals": pending})
}

type acceptSkillProposalReq struct {
	Keep []string `json:"keep"`
}

func (s *Server) handleAcceptSkillProposal(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	pid := r.PathValue("pid")
	if s.requireAgentOwner(w, r, agentID) == nil {
		return
	}
	var req acceptSkillProposalReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid body"})
		return
	}
	skillDir, err := resolveInstallTarget(r, agentID)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if err := agent.ApplyProposal(r.Context(), s.dataStore, pid, req.Keep, skillDir); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if ag := s.resolveAgent(r, agentID); ag != nil {
		ag.ReloadWorkspaceFiles()
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleRejectSkillProposal(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	pid := r.PathValue("pid")
	if s.requireAgentOwner(w, r, agentID) == nil {
		return
	}
	if err := s.dataStore.SetProposalStatus(r.Context(), pid, "rejected", time.Now().UTC().Format(time.RFC3339)); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleListArchivedSkills(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if s.requireAgentOwner(w, r, agentID) == nil {
		return
	}
	skillDir, err := resolveInstallTarget(r, agentID)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	list, err := agent.ListArchived(skillDir)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "archived": list})
}

func (s *Server) handleDeleteArchivedSkill(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	name := r.PathValue("name")
	archivedAt := r.URL.Query().Get("at")
	if s.requireAgentOwner(w, r, agentID) == nil {
		return
	}
	skillDir, err := resolveInstallTarget(r, agentID)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if err := agent.DeleteArchivedSkill(skillDir, archivedAt, name); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}
