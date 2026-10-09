package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/events"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/redis/go-redis/v9"
)

// 2026-10-09 coder(lq): Cache only permission GET responses for one human.
// Task and direct-parent generations preserve inheritance without evicting
// unrelated tasks; no task content or write authorization uses this cache.
type PermissionReadCache struct{ cache *FrequentReadCache }

func NewPermissionReadCache(rdb redis.UniversalClient, namespace string) *PermissionReadCache {
	return &PermissionReadCache{cache: NewFrequentReadCache(rdb, namespace+":permission-reads")}
}

func (c *PermissionReadCache) SetMetrics(metrics *obsmetrics.ReadCacheMetrics) {
	c.cache.Metrics = metrics
}

func permissionCacheEndpoint(path string) string {
	path = strings.TrimSuffix(path, "/")
	switch {
	case path == "/api/project-permission-roles":
		return "project_roles"
	case path == "/api/task-permission-roles":
		return "task_roles"
	case strings.HasPrefix(path, "/api/issues/") && strings.HasSuffix(path, "/effective-access"):
		return "effective_access"
	case strings.HasPrefix(path, "/api/issues/") && strings.HasSuffix(path, "/access-control"):
		return "access_control"
	case strings.HasPrefix(path, "/api/issues/") && strings.HasSuffix(path, "/access-grants"):
		return "issue_grants"
	case strings.HasPrefix(path, "/api/projects/") && strings.HasSuffix(path, "/access-grants"):
		return "project_grants"
	default:
		return "other"
	}
}

func permissionResourceScope(workspace, kind, id string) string {
	return organizationCacheID(workspace) + ":" + kind + ":" + organizationCacheID(id)
}

func (h *Handler) CachePermissionRead(next http.HandlerFunc) http.HandlerFunc {
	if h.PermissionReadCache == nil {
		return next
	}
	return h.cacheReadResponse(next, h.PermissionReadCache.cache, "permission", h.permissionReadGeneration)
}

func (h *Handler) permissionReadGeneration(r *http.Request, workspace string) (func(context.Context) (string, error), error) {
	if r.Method != http.MethodGet || permissionCacheEndpoint(r.URL.Path) == "other" {
		return nil, errors.New("not a permission read")
	}
	workspace = organizationCacheID(workspace)
	if workspace == "" {
		return nil, errors.New("missing permission workspace")
	}
	scopes := []string{workspace}
	if strings.HasPrefix(r.URL.Path, "/api/issues/") {
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			return nil, err
		}
		var parent, project, parentProject string
		// 2026-10-09 coder(lq): Re-read resource bindings on every permission
		// request, so a move/deletion cannot hit a previously authorized response.
		ctx, cancel := context.WithTimeout(r.Context(), frequentReadStoreTimeout)
		defer cancel()
		err = h.DB.QueryRow(ctx, `SELECT COALESCE(i.parent_issue_id::text, ''),
			COALESCE(i.project_id::text, ''), COALESCE(parent.project_id::text, '')
			FROM issue i LEFT JOIN issue parent
			  ON parent.id=i.parent_issue_id AND parent.workspace_id=i.workspace_id
			WHERE i.workspace_id=$1::uuid AND i.id=$2::uuid`, workspace, id.String()).Scan(&parent, &project, &parentProject)
		if err != nil {
			return nil, err
		}
		scopes = append(scopes, permissionResourceScope(workspace, "task", id.String()))
		if parent != "" {
			scopes = append(scopes, permissionResourceScope(workspace, "task", parent))
		}
		if project != "" {
			scopes = append(scopes, permissionResourceScope(workspace, "project", project))
		}
		if parentProject != "" && parentProject != project {
			scopes = append(scopes, permissionResourceScope(workspace, "project", parentProject))
		}
	} else if strings.HasPrefix(r.URL.Path, "/api/projects/") {
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			return nil, err
		}
		scopes = append(scopes, permissionResourceScope(workspace, "project", id.String()))
	}
	return func(ctx context.Context) (string, error) {
		versions := make([]string, 0, len(scopes))
		for _, scope := range scopes {
			version, err := h.PermissionReadCache.cache.generation(ctx, scope)
			if err != nil {
				return "", err
			}
			versions = append(versions, scope+"="+version)
		}
		return strings.Join(versions, "|"), nil
	}, nil
}

func (c *PermissionReadCache) invalidate(workspace, kind, id, reason string) {
	if c == nil {
		return
	}
	key, scope := c.cache.prefix+"generation", "global"
	if workspace != "" {
		scope = "workspace"
		name := organizationCacheID(workspace)
		if id != "" {
			if parsed, err := uuid.Parse(id); err == nil {
				name, scope = permissionResourceScope(workspace, kind, parsed.String()), kind
			}
		}
		key = c.cache.scopeGenerationKey(name)
	}
	ttl := time.Minute
	if scope == "global" {
		ttl = 0
	}
	c.cache.writeCacheGeneration(key, scope, reason, ttl, "permission")
}

// 2026-10-09 coder(lq): Unknown event payloads conservatively invalidate the
// workspace. Never infer an authorization scope from untrusted actor headers.
func permissionEventResourceID(e events.Event, kind string) string {
	payload, ok := e.Payload.(map[string]any)
	if !ok {
		return ""
	}
	if id, ok := payload[kind+"_id"].(string); ok {
		return id
	}
	if kind == "issue" {
		switch comment := payload["comment"].(type) {
		case CommentResponse:
			return comment.IssueID
		case map[string]any:
			id, _ := comment["issue_id"].(string)
			return id
		}
	}
	switch value := payload[kind].(type) {
	case IssueResponse:
		return value.ID
	case ProjectResponse:
		return value.ID
	case map[string]any:
		id, _ := value["id"].(string)
		return id
	}
	return ""
}

func (c *PermissionReadCache) Observe(e events.Event) {
	category := strings.SplitN(e.Type, ":", 2)[0]
	switch category {
	case "issue":
		c.invalidate(e.WorkspaceID, "task", permissionEventResourceID(e, "issue"), "issue")
	case "comment":
		if e.Type == "comment:created" || e.Type == "comment:updated" || e.Type == "comment:deleted" {
			c.invalidate(e.WorkspaceID, "task", permissionEventResourceID(e, "issue"), "issue")
		}
	case "project":
		c.invalidate(e.WorkspaceID, "project", permissionEventResourceID(e, "project"), "project")
	case "organization", "projectauth", "permission", "member", "workspace":
		c.invalidate(e.WorkspaceID, "", "", category)
	}
}

func (h *Handler) PermissionReadMutationMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.PermissionReadCache == nil || r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) < 2 || parts[0] != "api" {
			next.ServeHTTP(w, r)
			return
		}
		readPost := parts[1] == "issues" && len(parts) >= 3 && (parts[2] == "table" || parts[2] == "query")
		if readPost {
			next.ServeHTTP(w, r)
			return
		}
		kind, id, workspace := "", "", ""
		switch parts[1] {
		case "issues", "projects":
			workspace = h.resolveWorkspaceID(r)
			if parts[1] == "issues" {
				kind = "task"
			} else {
				kind = "project"
			}
			if len(parts) > 2 {
				id = parts[2]
			}
		case "workspaces":
			if len(parts) > 2 {
				if parsed, err := uuid.Parse(parts[2]); err == nil {
					workspace = parsed.String()
				}
			}
		case "project-permission-roles", "task-permission-roles", "comments", "agents":
			workspace = h.resolveWorkspaceID(r)
		default:
			next.ServeHTTP(w, r)
			return
		}
		h.PermissionReadCache.invalidate(workspace, kind, id, "mutation")
		defer h.PermissionReadCache.invalidate(workspace, kind, id, "mutation")
		next.ServeHTTP(w, r)
	})
}
