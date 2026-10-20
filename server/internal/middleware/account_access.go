package middleware

import (
	"errors"
	"github.com/multica-ai/multica/server/internal/auth"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"log/slog"
	"net/http"
)

// 2026-10-19 coder(lq): Run after every credential branch, including cache hits.
// The account gate is independent of workspace/project authorization caches.
func enforceAccountAccess(queries *db.Queries, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if queries != nil {
			token, _ := extractToken(r)
			err := auth.CheckAccountToken(r.Context(), queries, r.Header.Get("X-User-ID"), token)
			if err != nil {
				if errors.Is(err, auth.ErrTemporarilyDisabledUser) || errors.Is(err, auth.ErrSessionRevoked) {
					auth.ClearAuthCookies(w)
					writeError(w, http.StatusUnauthorized, err.Error())
				} else {
					slog.Warn("auth: account access unavailable", "path", r.URL.Path, "error", err)
					writeError(w, http.StatusServiceUnavailable, "account access unavailable")
				}
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
