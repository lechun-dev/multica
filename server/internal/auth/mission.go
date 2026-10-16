package auth

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/multica-ai/multica/server/internal/util"
)

const MissionReadSource = "lechun_mission_read"
const MissionReadIssuer = "missionos"
const MissionReadAudience = "newDingSDK"
const MissionReadTTL = 5 * time.Minute

// Mission read scopes are deliberately narrower than normal session scopes.
// agent_task_read follows the workspace's existing project/task ACLs, while
// agent_owner_read is honored only by the two internal agent task-read routes
// after those handlers re-check the current workspace member role.
const (
	MissionTaskReadScope  = "agent_task_read"
	MissionOwnerReadScope = "agent_owner_read"
)

type missionReadScopeContextKey struct{}

var missionTaskIdentifier = regexp.MustCompile(`^[A-Za-z0-9_]+-[1-9][0-9]*$`)

// MissionReadClaims never creates a browser session or a renewable login token.
func MissionReadClaims(userID, email, workspaceID string, now time.Time) jwt.MapClaims {
	return MissionReadClaimsWithScope(userID, email, workspaceID, MissionTaskReadScope, now)
}

func MissionReadClaimsWithScope(userID, email, workspaceID, scope string, now time.Time) jwt.MapClaims {
	return jwt.MapClaims{
		"sub": userID, "email": email, "workspace_id": workspaceID,
		"scope":  scope,
		"source": MissionReadSource, "iss": MissionReadIssuer, "aud": MissionReadAudience,
		"iat": now.Unix(), "exp": now.Add(MissionReadTTL).Unix(),
	}
}

func WithMissionReadScopeForRequest(ctx context.Context, scope string) context.Context {
	return context.WithValue(ctx, missionReadScopeContextKey{}, scope)
}

// MissionReadScope returns the validated scope placed in the request context by
// the authentication middleware. Handlers must not trust a client header for
// this decision.
func MissionReadScope(ctx context.Context) string {
	scope, _ := ctx.Value(missionReadScopeContextKey{}).(string)
	return scope
}

// ValidateMissionReadScope runs before any auth/session renewal and fails closed.
func ValidateMissionReadScope(r *http.Request, token *jwt.Token, claims jwt.MapClaims, fromCookie bool) bool {
	if fromCookie || token.Method != jwt.SigningMethodHS256 || r.Method != http.MethodGet {
		return false
	}
	issuer, _ := claims.GetIssuer()
	audience, _ := claims.GetAudience()
	if issuer != MissionReadIssuer || len(audience) != 1 || audience[0] != MissionReadAudience {
		return false
	}
	iat, err := claims.GetIssuedAt()
	if err != nil || iat == nil || iat.Time.After(time.Now().Add(5*time.Second)) {
		return false
	}
	exp, err := claims.GetExpirationTime()
	if err != nil || exp == nil || !exp.Time.After(iat.Time) || exp.Time.After(iat.Time.Add(MissionReadTTL)) {
		return false
	}
	bound, _ := claims["workspace_id"].(string)
	if _, err := util.ParseUUID(bound); err != nil || bound == "" || r.Header.Get("X-Workspace-ID") != bound {
		return false
	}
	if r.Header.Get("X-Workspace-Slug") != "" || r.URL.Query().Has("workspace_slug") {
		return false
	}
	for _, value := range r.URL.Query()["workspace_id"] {
		if value != bound {
			return false
		}
	}
	scope, _ := claims["scope"].(string)
	if scope == "" {
		// Tokens issued before scoped reads were introduced remain valid for
		// the original ordinary task-read surface, but never gain owner access.
		scope = MissionTaskReadScope
	}
	if scope != MissionTaskReadScope && scope != MissionOwnerReadScope {
		return false
	}
	path := strings.TrimSuffix(r.URL.Path, "/")
	isIssuePath := path == "/api/issues" || strings.HasPrefix(path, "/api/issues/")
	isAgentPath := path == "/internal/agent/issues" || strings.HasPrefix(path, "/internal/agent/issues/")
	if !isIssuePath && !isAgentPath {
		return false
	}
	if isAgentPath && scope != MissionTaskReadScope && scope != MissionOwnerReadScope {
		return false
	}
	if path == "/api/issues" || path == "/internal/agent/issues" {
		return true
	}
	prefix := "/api/issues/"
	if isAgentPath {
		prefix = "/internal/agent/issues/"
	}
	id := strings.TrimPrefix(path, prefix)
	if _, err := util.ParseUUID(id); err == nil {
		return true
	}
	return len(id) <= 128 && missionTaskIdentifier.MatchString(id)
}
