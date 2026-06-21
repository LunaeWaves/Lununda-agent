package setup

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/LunaeWaves/Lununda-agent/internal/agent"
	"github.com/LunaeWaves/Lununda-agent/internal/config"
	"github.com/LunaeWaves/Lununda-agent/internal/scope"
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

// saveMemoryMap JSON-roundtrips mem into the map[string]any shape that
// scope.SaveSetting expects.
func (s *Server) saveMemoryMap(r *http.Request, agentID string, mem config.MemoryCfg) error {
	raw, err := json.Marshal(mem)
	if err != nil {
		return err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	return scope.SaveSetting(r.Context(), s.dataStore, "", agentID, "memory", m)
}

func (s *Server) handleListStaleSkills(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	rec := s.requireAgentOwner(w, r, agentID)
	if rec == nil {
		return
	}
	var mem config.MemoryCfg
	_ = scope.SettingInto(r.Context(), s.dataStore, "memory", rec.UserID, agentID, &mem)
	cfg := mem.SkillEvolution
	skillDir, err := resolveInstallTarget(r, agentID)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	staleAfter := cfg.StaleAfter
	if staleAfter == 0 {
		staleAfter = 90 * 24 * time.Hour
	}
	stale, err := agent.StaleAgentSkills(s.dataStore, agentID, skillDir, staleAfter, cfg.Pinned)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "stale": stale})
}

func (s *Server) handleArchiveOneSkill(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	name := r.PathValue("name")
	if s.requireAgentOwner(w, r, agentID) == nil {
		return
	}
	skillDir, err := resolveInstallTarget(r, agentID)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if err := agent.ArchiveSkill(skillDir, name); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if ag := s.resolveAgent(r, agentID); ag != nil {
		ag.ReloadWorkspaceFiles()
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

type pinSkillReq struct {
	Pinned bool `json:"pinned"`
}

// handleListLastSessionByChannel returns the most recent session for
// (agent, channel) with a non-empty chat_id. Used by the skill-evolution
// notify picker to default chatID/accountID from the last conversation.
func (s *Server) handleListLastSessionByChannel(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	channel := r.URL.Query().Get("channel")
	if s.requireAgentOwner(w, r, agentID) == nil {
		return
	}
	if channel == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "channel required"})
		return
	}
	sess, err := s.dataStore.LastSessionByChannel(r.Context(), agentID, channel)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if sess == nil {
		jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "session": nil})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "session": sess})
}

func (s *Server) handleTogglePinSkill(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	name := r.PathValue("name")
	rec := s.requireAgentOwner(w, r, agentID)
	if rec == nil {
		return
	}
	var req pinSkillReq
	_ = json.NewDecoder(r.Body).Decode(&req)
	var mem config.MemoryCfg
	_ = scope.SettingInto(r.Context(), s.dataStore, "memory", rec.UserID, agentID, &mem)
	cfg := mem.SkillEvolution
	set := map[string]bool{}
	for _, p := range cfg.Pinned {
		set[p] = true
	}
	set[name] = req.Pinned
	out := []string{}
	for k, v := range set {
		if v {
			out = append(out, k)
		}
	}
	cfg.Pinned = out
	mem.SkillEvolution = cfg
	if err := s.saveMemoryMap(r, agentID, mem); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}
