package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const workspaceRuntimeProviderCodex = "codex"

type workspaceRuntimeModelResponse struct {
	ID                                  string             `json:"id"`
	WorkspaceID                         string             `json:"workspace_id"`
	RuntimeProvider                     string             `json:"runtime_provider"`
	ModelID                             string             `json:"model_id"`
	DisplayName                         string             `json:"display_name"`
	ModelProvider                       string             `json:"model_provider"`
	Description                         string             `json:"description"`
	ThinkingLevels                      []ThinkingLevel    `json:"thinking_levels"`
	DefaultThinkingLevel                string             `json:"default_thinking_level"`
	ServiceTiers                        []ModelServiceTier `json:"service_tiers"`
	SupportsExplicitStandardServiceTier bool               `json:"supports_explicit_standard_service_tier"`
	Enabled                             bool               `json:"enabled"`
	SortOrder                           int32              `json:"sort_order"`
	CreatedAt                           string             `json:"created_at"`
	UpdatedAt                           string             `json:"updated_at"`
}

type workspaceRuntimeModelRequest struct {
	RuntimeProvider                     *string             `json:"runtime_provider"`
	ModelID                             *string             `json:"model_id"`
	DisplayName                         *string             `json:"display_name"`
	ModelProvider                       *string             `json:"model_provider"`
	Description                         *string             `json:"description"`
	ThinkingLevels                      *[]ThinkingLevel    `json:"thinking_levels"`
	DefaultThinkingLevel                *string             `json:"default_thinking_level"`
	ServiceTiers                        *[]ModelServiceTier `json:"service_tiers"`
	SupportsExplicitStandardServiceTier *bool               `json:"supports_explicit_standard_service_tier"`
	Enabled                             *bool               `json:"enabled"`
	SortOrder                           *int32              `json:"sort_order"`
}

type workspaceRuntimeModelInput struct {
	RuntimeProvider                     string
	ModelID                             string
	DisplayName                         string
	ModelProvider                       string
	Description                         string
	ThinkingLevels                      []ThinkingLevel
	DefaultThinkingLevel                string
	ServiceTiers                        []ModelServiceTier
	SupportsExplicitStandardServiceTier bool
	Enabled                             bool
	SortOrder                           int32
}

// 2026-09-17 coder(lq): Keep newly created workspaces behaviorally aligned
// with the Grok entries that Codex previously hardcoded for every workspace.
func defaultWorkspaceRuntimeModelInputs() []workspaceRuntimeModelInput {
	return []workspaceRuntimeModelInput{
		{
			RuntimeProvider: workspaceRuntimeProviderCodex,
			ModelID:         "grok-4.6",
			DisplayName:     "Grok 4.6",
			ModelProvider:   "openai",
			Description:     "Grok 4.6 routed through the configured Codex API gateway.",
			ThinkingLevels: []ThinkingLevel{
				{Value: "low", Label: "Low"},
				{Value: "medium", Label: "Medium"},
				{Value: "high", Label: "High"},
				{Value: "xhigh", Label: "Extra high"},
			},
			ServiceTiers: []ModelServiceTier{},
			Enabled:      true,
			SortOrder:    10,
		},
		{
			RuntimeProvider: workspaceRuntimeProviderCodex,
			ModelID:         "grok-4.5",
			DisplayName:     "Grok 4.5",
			ModelProvider:   "openai",
			Description:     "Grok 4.5 routed through the configured Codex API gateway.",
			ThinkingLevels: []ThinkingLevel{
				{Value: "low", Label: "Low"},
				{Value: "medium", Label: "Medium"},
				{Value: "high", Label: "High"},
			},
			ServiceTiers: []ModelServiceTier{},
			Enabled:      true,
			SortOrder:    20,
		},
	}
}

func createWorkspaceRuntimeModelRow(ctx context.Context, queries *db.Queries, workspaceID pgtype.UUID, input workspaceRuntimeModelInput) (db.WorkspaceRuntimeModel, error) {
	thinkingLevels, _ := json.Marshal(input.ThinkingLevels)
	serviceTiers, _ := json.Marshal(input.ServiceTiers)
	return queries.CreateWorkspaceRuntimeModel(ctx, db.CreateWorkspaceRuntimeModelParams{
		WorkspaceID:                         workspaceID,
		RuntimeProvider:                     input.RuntimeProvider,
		ModelID:                             input.ModelID,
		DisplayName:                         input.DisplayName,
		ModelProvider:                       input.ModelProvider,
		Description:                         input.Description,
		ThinkingLevels:                      thinkingLevels,
		DefaultThinkingLevel:                input.DefaultThinkingLevel,
		ServiceTiers:                        serviceTiers,
		SupportsExplicitStandardServiceTier: input.SupportsExplicitStandardServiceTier,
		Enabled:                             input.Enabled,
		SortOrder:                           input.SortOrder,
	})
}

func seedDefaultWorkspaceRuntimeModels(ctx context.Context, queries *db.Queries, workspaceID pgtype.UUID) error {
	for _, input := range defaultWorkspaceRuntimeModelInputs() {
		if _, err := createWorkspaceRuntimeModelRow(ctx, queries, workspaceID, input); err != nil {
			return err
		}
	}
	return nil
}

func workspaceRuntimeModelResponseFor(model db.WorkspaceRuntimeModel) workspaceRuntimeModelResponse {
	thinkingLevels := make([]ThinkingLevel, 0)
	if len(model.ThinkingLevels) > 0 {
		_ = json.Unmarshal(model.ThinkingLevels, &thinkingLevels)
	}
	serviceTiers := make([]ModelServiceTier, 0)
	if len(model.ServiceTiers) > 0 {
		_ = json.Unmarshal(model.ServiceTiers, &serviceTiers)
	}
	return workspaceRuntimeModelResponse{
		ID:                                  uuidToString(model.ID),
		WorkspaceID:                         uuidToString(model.WorkspaceID),
		RuntimeProvider:                     model.RuntimeProvider,
		ModelID:                             model.ModelID,
		DisplayName:                         model.DisplayName,
		ModelProvider:                       model.ModelProvider,
		Description:                         model.Description,
		ThinkingLevels:                      thinkingLevels,
		DefaultThinkingLevel:                model.DefaultThinkingLevel,
		ServiceTiers:                        serviceTiers,
		SupportsExplicitStandardServiceTier: model.SupportsExplicitStandardServiceTier,
		Enabled:                             model.Enabled,
		SortOrder:                           model.SortOrder,
		CreatedAt:                           timestampToString(model.CreatedAt),
		UpdatedAt:                           timestampToString(model.UpdatedAt),
	}
}

func workspaceRuntimeModelInputFor(model db.WorkspaceRuntimeModel) workspaceRuntimeModelInput {
	response := workspaceRuntimeModelResponseFor(model)
	return workspaceRuntimeModelInput{
		RuntimeProvider:                     response.RuntimeProvider,
		ModelID:                             response.ModelID,
		DisplayName:                         response.DisplayName,
		ModelProvider:                       response.ModelProvider,
		Description:                         response.Description,
		ThinkingLevels:                      response.ThinkingLevels,
		DefaultThinkingLevel:                response.DefaultThinkingLevel,
		ServiceTiers:                        response.ServiceTiers,
		SupportsExplicitStandardServiceTier: response.SupportsExplicitStandardServiceTier,
		Enabled:                             response.Enabled,
		SortOrder:                           response.SortOrder,
	}
}

func normalizeWorkspaceRuntimeModelInput(req workspaceRuntimeModelRequest, current *workspaceRuntimeModelInput) (workspaceRuntimeModelInput, error) {
	input := workspaceRuntimeModelInput{
		RuntimeProvider: workspaceRuntimeProviderCodex,
		ModelProvider:   "openai",
		ThinkingLevels:  []ThinkingLevel{},
		ServiceTiers:    []ModelServiceTier{},
		Enabled:         true,
	}
	if current != nil {
		input = *current
	}
	if req.RuntimeProvider != nil {
		input.RuntimeProvider = strings.TrimSpace(*req.RuntimeProvider)
	}
	if req.ModelID != nil {
		input.ModelID = strings.TrimSpace(*req.ModelID)
	}
	if req.DisplayName != nil {
		input.DisplayName = strings.TrimSpace(*req.DisplayName)
	}
	if req.ModelProvider != nil {
		input.ModelProvider = strings.TrimSpace(*req.ModelProvider)
	}
	if req.Description != nil {
		input.Description = strings.TrimSpace(*req.Description)
	}
	if req.ThinkingLevels != nil {
		input.ThinkingLevels = append([]ThinkingLevel(nil), (*req.ThinkingLevels)...)
	}
	if req.DefaultThinkingLevel != nil {
		input.DefaultThinkingLevel = strings.TrimSpace(*req.DefaultThinkingLevel)
	}
	if req.ServiceTiers != nil {
		input.ServiceTiers = append([]ModelServiceTier(nil), (*req.ServiceTiers)...)
	}
	if req.SupportsExplicitStandardServiceTier != nil {
		input.SupportsExplicitStandardServiceTier = *req.SupportsExplicitStandardServiceTier
	}
	if req.Enabled != nil {
		input.Enabled = *req.Enabled
	}
	if req.SortOrder != nil {
		input.SortOrder = *req.SortOrder
	}

	if input.RuntimeProvider != workspaceRuntimeProviderCodex {
		return workspaceRuntimeModelInput{}, invalidWorkspaceRuntimeModelError("runtime_provider currently supports codex only")
	}
	if input.ModelID == "" || len(input.ModelID) > 120 {
		return workspaceRuntimeModelInput{}, invalidWorkspaceRuntimeModelError("model_id is required and must be at most 120 characters")
	}
	if input.DisplayName == "" || len(input.DisplayName) > 120 {
		return workspaceRuntimeModelInput{}, invalidWorkspaceRuntimeModelError("display_name is required and must be at most 120 characters")
	}
	if input.ModelProvider == "" || len(input.ModelProvider) > 120 {
		return workspaceRuntimeModelInput{}, invalidWorkspaceRuntimeModelError("model_provider is required and must be at most 120 characters")
	}
	if len(input.Description) > 1000 {
		return workspaceRuntimeModelInput{}, invalidWorkspaceRuntimeModelError("description must be at most 1000 characters")
	}
	if input.SortOrder < 0 {
		return workspaceRuntimeModelInput{}, invalidWorkspaceRuntimeModelError("sort_order must be non-negative")
	}

	thinkingValues := make(map[string]struct{}, len(input.ThinkingLevels))
	for i := range input.ThinkingLevels {
		level := &input.ThinkingLevels[i]
		level.Value = strings.TrimSpace(level.Value)
		level.Label = strings.TrimSpace(level.Label)
		level.Description = strings.TrimSpace(level.Description)
		if level.Value == "" || level.Label == "" || len(level.Value) > 80 || len(level.Label) > 120 || len(level.Description) > 500 {
			return workspaceRuntimeModelInput{}, invalidWorkspaceRuntimeModelError("each thinking level requires a valid value and label")
		}
		if _, exists := thinkingValues[level.Value]; exists {
			return workspaceRuntimeModelInput{}, invalidWorkspaceRuntimeModelError("thinking level values must be unique")
		}
		thinkingValues[level.Value] = struct{}{}
	}
	if input.DefaultThinkingLevel != "" {
		if _, exists := thinkingValues[input.DefaultThinkingLevel]; !exists {
			return workspaceRuntimeModelInput{}, invalidWorkspaceRuntimeModelError("default_thinking_level must be one of thinking_levels")
		}
	}

	tierIDs := make(map[string]struct{}, len(input.ServiceTiers))
	for i := range input.ServiceTiers {
		tier := &input.ServiceTiers[i]
		tier.ID = strings.TrimSpace(tier.ID)
		tier.Name = strings.TrimSpace(tier.Name)
		tier.Description = strings.TrimSpace(tier.Description)
		if tier.ID == "" || tier.Name == "" || len(tier.ID) > 80 || len(tier.Name) > 120 || len(tier.Description) > 500 {
			return workspaceRuntimeModelInput{}, invalidWorkspaceRuntimeModelError("each service tier requires a valid id and name")
		}
		if _, exists := tierIDs[tier.ID]; exists {
			return workspaceRuntimeModelInput{}, invalidWorkspaceRuntimeModelError("service tier ids must be unique")
		}
		tierIDs[tier.ID] = struct{}{}
	}
	return input, nil
}

type invalidWorkspaceRuntimeModelError string

func (e invalidWorkspaceRuntimeModelError) Error() string { return string(e) }

func (h *Handler) ListWorkspaceRuntimeModels(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	models, err := h.Queries.ListWorkspaceRuntimeModels(r.Context(), workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list runtime models")
		return
	}
	response := make([]workspaceRuntimeModelResponse, 0, len(models))
	for _, model := range models {
		response = append(response, workspaceRuntimeModelResponseFor(model))
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) CreateWorkspaceRuntimeModel(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	var req workspaceRuntimeModelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	input, err := normalizeWorkspaceRuntimeModelInput(req, nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	model, err := createWorkspaceRuntimeModelRow(r.Context(), h.Queries, workspaceID, input)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "this model already exists for the runtime")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create runtime model")
		return
	}
	writeJSON(w, http.StatusCreated, workspaceRuntimeModelResponseFor(model))
}

func (h *Handler) UpdateWorkspaceRuntimeModel(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	modelRowID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "modelId"), "runtime model id")
	if !ok {
		return
	}
	existing, err := h.Queries.GetWorkspaceRuntimeModel(r.Context(), db.GetWorkspaceRuntimeModelParams{ID: modelRowID, WorkspaceID: workspaceID})
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "runtime model not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load runtime model")
		return
	}
	var req workspaceRuntimeModelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// 2026-09-17 coder(lq): Keys stay immutable so agent references and catalog
	// cache identities cannot silently move to a different model.
	if req.RuntimeProvider != nil && strings.TrimSpace(*req.RuntimeProvider) != existing.RuntimeProvider {
		writeError(w, http.StatusBadRequest, "runtime_provider cannot be changed")
		return
	}
	if req.ModelID != nil && strings.TrimSpace(*req.ModelID) != existing.ModelID {
		writeError(w, http.StatusBadRequest, "model_id cannot be changed")
		return
	}
	current := workspaceRuntimeModelInputFor(existing)
	input, err := normalizeWorkspaceRuntimeModelInput(req, &current)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if existing.Enabled && !input.Enabled {
		inUse, err := h.workspaceRuntimeModelUsageCount(r, existing)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to check runtime model usage")
			return
		}
		if inUse > 0 {
			writeError(w, http.StatusConflict, "runtime model is currently used by agents")
			return
		}
	}
	thinkingLevels, _ := json.Marshal(input.ThinkingLevels)
	serviceTiers, _ := json.Marshal(input.ServiceTiers)
	model, err := h.Queries.UpdateWorkspaceRuntimeModel(r.Context(), db.UpdateWorkspaceRuntimeModelParams{
		ID:                                  modelRowID,
		WorkspaceID:                         workspaceID,
		DisplayName:                         input.DisplayName,
		ModelProvider:                       input.ModelProvider,
		Description:                         input.Description,
		ThinkingLevels:                      thinkingLevels,
		DefaultThinkingLevel:                input.DefaultThinkingLevel,
		ServiceTiers:                        serviceTiers,
		SupportsExplicitStandardServiceTier: input.SupportsExplicitStandardServiceTier,
		Enabled:                             input.Enabled,
		SortOrder:                           input.SortOrder,
	})
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "runtime model not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update runtime model")
		return
	}
	writeJSON(w, http.StatusOK, workspaceRuntimeModelResponseFor(model))
}

func (h *Handler) DeleteWorkspaceRuntimeModel(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	modelRowID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "modelId"), "runtime model id")
	if !ok {
		return
	}
	existing, err := h.Queries.GetWorkspaceRuntimeModel(r.Context(), db.GetWorkspaceRuntimeModelParams{ID: modelRowID, WorkspaceID: workspaceID})
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "runtime model not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load runtime model")
		return
	}
	inUse, err := h.workspaceRuntimeModelUsageCount(r, existing)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check runtime model usage")
		return
	}
	if inUse > 0 {
		writeError(w, http.StatusConflict, "runtime model is currently used by agents")
		return
	}
	deleted, err := h.Queries.DeleteWorkspaceRuntimeModel(r.Context(), db.DeleteWorkspaceRuntimeModelParams{ID: modelRowID, WorkspaceID: workspaceID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete runtime model")
		return
	}
	if deleted == 0 {
		writeError(w, http.StatusNotFound, "runtime model not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) workspaceRuntimeModelUsageCount(r *http.Request, model db.WorkspaceRuntimeModel) (int64, error) {
	return h.Queries.CountAgentsUsingWorkspaceRuntimeModel(r.Context(), db.CountAgentsUsingWorkspaceRuntimeModelParams{
		WorkspaceID: model.WorkspaceID,
		Provider:    model.RuntimeProvider,
		Model:       pgtype.Text{String: model.ModelID, Valid: true},
	})
}
