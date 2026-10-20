package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/gorilla/websocket"
	"github.com/multica-ai/multica/server/internal/auth"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// 2026-10-19 coder(lq): Existing sockets must lose access too, on every replica.
// No reliance on a best-effort logout event or workspace cache invalidation.
const accountAccessCheckInterval = 5 * time.Second

// SetAccountQueries is configured before accepting any connections.
func (h *Hub) SetAccountQueries(queries *db.Queries) { h.accountQueries = queries }

func (h *Hub) authenticateAccount(token string, resolver PATResolver, ctx context.Context) (string, string) {
	uid, message := authenticateToken(token, resolver, ctx)
	if message != "" || h.accountQueries == nil {
		return uid, message
	}
	if err := auth.CheckAccountToken(ctx, h.accountQueries, uid, token); err != nil {
		if errors.Is(err, auth.ErrTemporarilyDisabledUser) || errors.Is(err, auth.ErrSessionRevoked) {
			frame, _ := json.Marshal(map[string]any{"type": "account:disabled", "error": "account disabled", "payload": map[string]string{"user_id": uid}})
			return "", string(frame)
		}
		return "", `{"error":"account access unavailable"}`
	}
	return uid, ""
}

func (c *Client) accountAccessAllowed() bool {
	if c.accountCheck == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := c.accountCheck(ctx)
	if err == nil {
		return true
	}
	if errors.Is(err, auth.ErrTemporarilyDisabledUser) || errors.Is(err, auth.ErrSessionRevoked) {
		c.conn.SetWriteDeadline(time.Now().Add(writeWait))
		// The user-specific frame is emitted by the single writer; no business
		// payload may be delivered after a failed account check.
		_ = c.conn.WriteJSON(map[string]any{"type": "account:disabled", "payload": map[string]string{"user_id": c.userID}})
		_ = c.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "account access revoked"))
	}
	return false
}
