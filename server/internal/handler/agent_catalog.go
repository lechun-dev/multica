package handler

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ownerAgentCatalogEntry is a display-only projection. Never embed AgentResponse:
// this endpoint covers agents whose normal detail and actions are denied.
type ownerAgentCatalogEntry struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	OwnerID     *string    `json:"owner_id"`
	ArchivedAt  *time.Time `json:"archived_at"`
	CreatedAt   time.Time  `json:"created_at"`
	AccessScope string     `json:"access_scope"`
}

type ownerAgentDetail struct {
	ownerAgentCatalogEntry
	Description        string        `json:"description"`
	UpdatedAt          string        `json:"updated_at"`
	Model              string        `json:"model"`
	ThinkingLevel      string        `json:"thinking_level"`
	MaxConcurrentTasks int32         `json:"max_concurrent_tasks"`
	RuntimeBound       bool          `json:"runtime_bound"`
	Profile            AgentResponse `json:"profile"`
}

func (h *Handler) requireHumanAgentCatalogOwner(w http.ResponseWriter, r *http.Request, workspaceID string) bool {
	w.Header().Set("Cache-Control", "private, no-store")
	if _, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner"); !ok {
		return false
	}
	if actorType, _ := h.resolveActor(r, requestUserID(r), workspaceID); actorType != "member" {
		writeError(w, http.StatusForbidden, "agent catalog requires a workspace owner")
		return false
	}
	return true
}

func agentCatalogEntry(agent db.Agent, targets []db.AgentInvocationTarget) ownerAgentCatalogEntry {
	scope := "owner-only"
	if agent.PermissionMode == permissionModePublicTo {
		scope = "specific-people"
		if deriveLegacyVisibility(agent.PermissionMode, targets) == "workspace" {
			scope = "workspace"
		}
	}
	entry := ownerAgentCatalogEntry{
		ID: uuidToString(agent.ID), Name: agent.Name, CreatedAt: agent.CreatedAt.Time, AccessScope: scope,
	}
	if agent.OwnerID.Valid {
		ownerID := uuidToString(agent.OwnerID)
		entry.OwnerID = &ownerID
	}
	if agent.ArchivedAt.Valid {
		entry.ArchivedAt = &agent.ArchivedAt.Time
	}
	return entry
}

// GetOwnerAgentDetail serves the read-only profile separately from invocation.
func (h *Handler) GetOwnerAgentDetail(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if !h.requireHumanAgentCatalogOwner(w, r, workspaceID) {
		return
	}
	agentID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "agent id")
	if !ok {
		return
	}
	ctx := r.Context()
	agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID: agentID, WorkspaceID: parseUUID(workspaceID),
	})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && agent.Kind != "user") {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load agent")
		return
	}
	targets, ok := h.loadInvocationTargetsByAgent(ctx, []db.Agent{agent})
	if !ok {
		writeError(w, http.StatusInternalServerError, "failed to load agent invocation targets")
		return
	}
	profile := h.agentToResponse(agent)
	applyInvocationTargetsToResponse(&profile, targets[profile.ID])
	if err := h.attachAgentSkills(ctx, &profile, agent.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load agent skills")
		return
	}
	// The raw MCP document can contain credentials in arbitrary provider
	// fields. The separate binding inventory contains only names and status.
	redactMcpConfig(&profile)
	if !h.composioMCPAppsEnabled(ctx) {
		suppressComposioToolkitAllowlist(&profile)
	}
	writeJSON(w, http.StatusOK, ownerAgentDetail{
		Profile:                profile,
		ownerAgentCatalogEntry: agentCatalogEntry(agent, targets[uuidToString(agent.ID)]),
		Description:            agent.Description, UpdatedAt: timestampToString(agent.UpdatedAt),
		Model: agent.Model.String, ThinkingLevel: agent.ThinkingLevel.String,
		MaxConcurrentTasks: agent.MaxConcurrentTasks, RuntimeBound: agent.RuntimeID.Valid,
	})
}

// ListOwnerAgentCatalog supplements the normal list for human workspace owners.
// Its results must never be treated as evidence of inspection or invocation access.
func (h *Handler) ListOwnerAgentCatalog(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if !h.requireHumanAgentCatalogOwner(w, r, workspaceID) {
		return
	}
	userID := requestUserID(r)
	ctx := r.Context()
	agents, err := h.Queries.ListAllAgents(ctx, parseUUID(workspaceID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list agent catalog")
		return
	}
	targetsByAgent, ok := h.loadInvocationTargetsByAgent(ctx, agents)
	if !ok {
		writeError(w, http.StatusInternalServerError, "failed to load agent invocation targets")
		return
	}
	entries := make([]ownerAgentCatalogEntry, 0, len(agents))
	for _, agent := range agents {
		id := uuidToString(agent.ID)
		if memberAllowedToViewAgent(agent, targetsByAgent[id], userID, "owner") {
			continue
		}
		entries = append(entries, agentCatalogEntry(agent, targetsByAgent[id]))
	}
	writeJSON(w, http.StatusOK, entries)
}
