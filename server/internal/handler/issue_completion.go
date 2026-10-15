package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// Completion is the latest entry into done, not a deadline or last update.
// Reopened/cancelled tasks and tasks without transition history return NULL.
func issueCompletedAtSQL(alias string) string {
	return fmt.Sprintf(`CASE WHEN issue_effective_status(%[1]s.workspace_id, %[1]s.status) = 'done' THEN (
SELECT MAX(a.created_at) FROM activity_log a
WHERE a.workspace_id = %[1]s.workspace_id AND a.issue_id = %[1]s.id AND a.action = 'status_changed'
AND COALESCE(a.details->>'from', '') <> ''
AND issue_effective_status(%[1]s.workspace_id, a.details->>'to') = 'done'
AND issue_effective_status(%[1]s.workspace_id, a.details->>'from') IS DISTINCT FROM 'done'
) END`, alias)
}

type issueTimeRange struct {
	expression string
	from, to   *time.Time
}

func parseIssueTimeRanges(w http.ResponseWriter, values url.Values) ([]issueTimeRange, bool) {
	var ranges []issueTimeRange
	for _, field := range []string{"created", "completed"} {
		entry := issueTimeRange{expression: "i.created_at"}
		if field == "completed" {
			entry.expression = issueCompletedAtSQL("i")
		}
		for _, bound := range []string{"from", "to"} {
			key := field + "_" + bound
			if !values.Has(key) {
				continue
			}
			parsed, err := time.Parse(time.RFC3339Nano, values.Get(key))
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid "+key+": RFC3339 required")
				return nil, false
			}
			if bound == "from" {
				entry.from = &parsed
			} else {
				entry.to = &parsed
			}
		}
		if entry.from != nil && entry.to != nil && !entry.from.Before(*entry.to) {
			writeError(w, http.StatusBadRequest, field+"_from must precede "+field+"_to")
			return nil, false
		}
		if entry.from != nil || entry.to != nil {
			ranges = append(ranges, entry)
		}
	}
	return ranges, true
}

func appendIssueTimeRanges(where []string, addArg func(any) string, ranges []issueTimeRange) []string {
	for _, entry := range ranges {
		if entry.from != nil {
			where = append(where, "("+entry.expression+") >= "+addArg(*entry.from)+"::timestamptz")
		}
		if entry.to != nil {
			where = append(where, "("+entry.expression+") < "+addArg(*entry.to)+"::timestamptz")
		}
	}
	return where
}

// One scoped query for the authorized page; never an N+1 activity lookup.
func (h *Handler) fillIssueCompletion(ctx context.Context, workspaceID pgtype.UUID, responses []IssueResponse) error {
	if len(responses) == 0 {
		return nil
	}
	ids := make([]pgtype.UUID, len(responses))
	for i := range responses {
		ids[i] = parseUUID(responses[i].ID)
	}
	rows, err := h.DB.Query(ctx, "SELECT i.id, "+issueCompletedAtSQL("i")+" FROM issue i WHERE i.workspace_id = $1 AND i.id = ANY($2::uuid[])", workspaceID, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	completed := map[string]*string{}
	for rows.Next() {
		var id pgtype.UUID
		var timestamp pgtype.Timestamptz
		if err := rows.Scan(&id, &timestamp); err != nil {
			return err
		}
		var value *string
		if timestamp.Valid {
			formatted := timestamp.Time.UTC().Format(time.RFC3339Nano)
			value = &formatted
		}
		completed[uuidToString(id)] = value
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range responses {
		value := completed[responses[i].ID]
		responses[i].CompletedAt = &value
	}
	return nil
}
