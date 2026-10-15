package auth

import (
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

var missionTaskIdentifier = regexp.MustCompile(`^[A-Za-z0-9_]+-[1-9][0-9]*$`)

// MissionReadClaims never creates a browser session or a renewable login token.
func MissionReadClaims(userID, email, workspaceID string, now time.Time) jwt.MapClaims {
	return jwt.MapClaims{
		"sub": userID, "email": email, "workspace_id": workspaceID,
		"source": MissionReadSource, "iss": MissionReadIssuer, "aud": MissionReadAudience,
		"iat": now.Unix(), "exp": now.Add(MissionReadTTL).Unix(),
	}
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
	path := strings.TrimSuffix(r.URL.Path, "/")
	if path == "/api/issues" {
		return true
	}
	if !strings.HasPrefix(path, "/api/issues/") {
		return false
	}
	id := strings.TrimPrefix(path, "/api/issues/")
	if _, err := util.ParseUUID(id); err == nil {
		return true
	}
	return len(id) <= 128 && missionTaskIdentifier.MatchString(id)
}
