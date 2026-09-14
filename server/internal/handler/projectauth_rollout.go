package handler

import (
	"net/http"
)

// requireProjectAuthorizationWriter is the common mutation gate. Reader is
// intentionally not enough: operators can roll back from writer/restricted to
// reader to stop all policy changes without disabling enforcement for tasks
// that are already restricted.
func (h *Handler) requireProjectAuthorizationWriter(w http.ResponseWriter, restricted bool) bool {
	if h.ProjectAuth == nil || (!h.ProjectAuth.Enabled() && !h.ProjectAuth.ShadowEnabled()) {
		h.Metrics.RecordProjectAuthorizationDecision("write", "disabled")
		writeErrorCode(w, http.StatusNotFound, "project_permission_disabled", "project permissions are disabled")
		return false
	}
	if !h.ProjectAuth.WriterEnabled() {
		h.Metrics.RecordProjectAuthorizationDecision("write", "disabled")
		writeErrorCode(w, http.StatusServiceUnavailable, "project_permission_writes_disabled", "project permission writes are disabled during rollout")
		return false
	}
	if restricted && !h.ProjectAuth.RestrictedWritesEnabled() {
		h.Metrics.RecordProjectAuthorizationDecision("write", "disabled")
		writeErrorCode(w, http.StatusServiceUnavailable, "project_permission_restricted_writes_disabled", "restricted task writes are disabled during rollout")
		return false
	}
	h.Metrics.RecordProjectAuthorizationDecision("write", "allow")
	return true
}
