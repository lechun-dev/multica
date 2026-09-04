package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type taskRetryPolicyResponse struct {
	ID            string          `json:"id"`
	WorkspaceID   string          `json:"workspace_id"`
	Name          string          `json:"name"`
	Enabled       bool            `json:"enabled"`
	Priority      int32           `json:"priority"`
	MatchType     string          `json:"match_type"`
	MatchValue    string          `json:"match_value"`
	MaxAttempts   int32           `json:"max_attempts"`
	DelaySchedule json.RawMessage `json:"delay_schedule"`
	CreatedAt     string          `json:"created_at"`
	UpdatedAt     string          `json:"updated_at"`
}

func taskRetryPolicyResponseFor(p db.TaskRetryPolicy) taskRetryPolicyResponse {
	delay := json.RawMessage(p.DelaySchedule)
	if len(delay) == 0 {
		delay = json.RawMessage("[0]")
	}
	return taskRetryPolicyResponse{ID: uuidToString(p.ID), WorkspaceID: uuidToString(p.WorkspaceID), Name: p.Name, Enabled: p.Enabled, Priority: p.Priority, MatchType: p.MatchType, MatchValue: p.MatchValue, MaxAttempts: p.MaxAttempts, DelaySchedule: delay, CreatedAt: timestampToString(p.CreatedAt), UpdatedAt: timestampToString(p.UpdatedAt)}
}

type taskRetryPolicyRequest struct {
	Name          *string         `json:"name"`
	Enabled       *bool           `json:"enabled"`
	Priority      *int32          `json:"priority"`
	MatchType     *string         `json:"match_type"`
	MatchValue    *string         `json:"match_value"`
	MaxAttempts   *int32          `json:"max_attempts"`
	DelaySchedule json.RawMessage `json:"delay_schedule"`
}

func parseTaskRetryPolicyInput(req taskRetryPolicyRequest, partial bool) (db.CreateTaskRetryPolicyParams, error) {
	name := ""
	if req.Name != nil {
		name = strings.TrimSpace(*req.Name)
		if name == "" || len(name) > 120 {
			return db.CreateTaskRetryPolicyParams{}, errInvalidTaskRetryPolicy("name is required and must be at most 120 characters")
		}
	} else if !partial {
		return db.CreateTaskRetryPolicyParams{}, errInvalidTaskRetryPolicy("name is required and must be at most 120 characters")
	}

	matchType := "failure_reason"
	if req.MatchType != nil {
		matchType = strings.TrimSpace(*req.MatchType)
		if matchType != "failure_reason" && matchType != "http_status" && matchType != "error_contains" {
			return db.CreateTaskRetryPolicyParams{}, errInvalidTaskRetryPolicy("match_type must be failure_reason, http_status, or error_contains")
		}
	} else if !partial {
		return db.CreateTaskRetryPolicyParams{}, errInvalidTaskRetryPolicy("match_type must be failure_reason, http_status, or error_contains")
	}

	matchValue := ""
	if req.MatchValue != nil {
		matchValue = strings.TrimSpace(*req.MatchValue)
		if matchValue == "" || len(matchValue) > 500 {
			return db.CreateTaskRetryPolicyParams{}, errInvalidTaskRetryPolicy("match_value is required and must be at most 500 characters")
		}
	} else if !partial {
		return db.CreateTaskRetryPolicyParams{}, errInvalidTaskRetryPolicy("match_value is required and must be at most 500 characters")
	}
	max := int32(2)
	if req.MaxAttempts != nil {
		max = *req.MaxAttempts
	}
	if max < 1 || max > 5 {
		return db.CreateTaskRetryPolicyParams{}, errInvalidTaskRetryPolicy("max_attempts must be between 1 and 5")
	}
	priority := int32(100)
	if req.Priority != nil {
		priority = *req.Priority
	}
	if priority < 0 {
		return db.CreateTaskRetryPolicyParams{}, errInvalidTaskRetryPolicy("priority must be non-negative")
	}
	delay := req.DelaySchedule
	if len(delay) == 0 {
		delay = json.RawMessage("[0]")
	}
	var values []int
	if err := json.Unmarshal(delay, &values); err != nil || len(values) > 10 {
		return db.CreateTaskRetryPolicyParams{}, errInvalidTaskRetryPolicy("delay_schedule must be an array of at most 10 integers")
	}
	for _, v := range values {
		if v < 0 || v > 86400 {
			return db.CreateTaskRetryPolicyParams{}, errInvalidTaskRetryPolicy("delay values must be between 0 and 86400 seconds")
		}
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	return db.CreateTaskRetryPolicyParams{Name: name, Enabled: enabled, Priority: priority, MatchType: matchType, MatchValue: matchValue, MaxAttempts: max, DelaySchedule: delay}, nil
}

type invalidTaskRetryPolicyError string

func (e invalidTaskRetryPolicyError) Error() string { return string(e) }
func errInvalidTaskRetryPolicy(s string) error      { return invalidTaskRetryPolicyError(s) }

func (h *Handler) ListTaskRetryPolicies(w http.ResponseWriter, r *http.Request) {
	workspaceID, err := util.ParseUUID(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid workspace id")
		return
	}
	items, err := h.Queries.ListTaskRetryPolicies(r.Context(), workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list retry policies")
		return
	}
	response := make([]taskRetryPolicyResponse, 0, len(items))
	for _, item := range items {
		response = append(response, taskRetryPolicyResponseFor(item))
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) CreateTaskRetryPolicy(w http.ResponseWriter, r *http.Request) {
	workspaceID, err := util.ParseUUID(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid workspace id")
		return
	}
	var req taskRetryPolicyRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	input, err := parseTaskRetryPolicyInput(req, false)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	input.WorkspaceID = workspaceID
	if member, ok := ctxMember(r.Context()); ok {
		input.CreatedBy = member.UserID
	}
	item, err := h.Queries.CreateTaskRetryPolicy(r.Context(), input)
	if err != nil {
		writeError(w, http.StatusConflict, "retry policy could not be created")
		return
	}
	writeJSON(w, http.StatusCreated, taskRetryPolicyResponseFor(item))
}

func (h *Handler) UpdateTaskRetryPolicy(w http.ResponseWriter, r *http.Request) {
	workspaceID, err := util.ParseUUID(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid workspace id")
		return
	}
	id, err := util.ParseUUID(chi.URLParam(r, "policyId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid policy id")
		return
	}
	var req taskRetryPolicyRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	input, err := parseTaskRetryPolicyInput(req, true)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	params := db.UpdateTaskRetryPolicyParams{ID: id, WorkspaceID: workspaceID, Name: pgtype.Text{String: input.Name, Valid: req.Name != nil}, Enabled: pgtype.Bool{Bool: input.Enabled, Valid: req.Enabled != nil}, Priority: pgtype.Int4{Int32: input.Priority, Valid: req.Priority != nil}, MatchType: pgtype.Text{String: input.MatchType, Valid: req.MatchType != nil}, MatchValue: pgtype.Text{String: input.MatchValue, Valid: req.MatchValue != nil}, MaxAttempts: pgtype.Int4{Int32: input.MaxAttempts, Valid: req.MaxAttempts != nil}, DelaySchedule: nil}
	if req.DelaySchedule != nil {
		params.DelaySchedule = req.DelaySchedule
	}
	item, err := h.Queries.UpdateTaskRetryPolicy(r.Context(), params)
	if err != nil {
		writeError(w, http.StatusNotFound, "retry policy not found")
		return
	}
	writeJSON(w, http.StatusOK, taskRetryPolicyResponseFor(item))
}

func (h *Handler) DeleteTaskRetryPolicy(w http.ResponseWriter, r *http.Request) {
	workspaceID, err := util.ParseUUID(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid workspace id")
		return
	}
	id, err := util.ParseUUID(chi.URLParam(r, "policyId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid policy id")
		return
	}
	if err := h.Queries.DeleteTaskRetryPolicy(r.Context(), db.DeleteTaskRetryPolicyParams{ID: id, WorkspaceID: workspaceID}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete retry policy")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
