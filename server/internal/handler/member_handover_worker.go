package handler

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/internal/auth"
)

// Wake is a hint, not a work queue. Database evidence survives process restarts
// and compensation scans recover dropped/coalesced hints in every server replica.
type MemberHandoverWorker struct {
	h    *Handler
	wake chan struct{}
}

func NewMemberHandoverWorker(h *Handler) *MemberHandoverWorker {
	return &MemberHandoverWorker{h: h, wake: make(chan struct{}, 1)}
}
func (w *MemberHandoverWorker) Wake() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}
func (h *Handler) WakeMemberHandovers() {
	if h.MemberHandoverWorker != nil {
		h.MemberHandoverWorker.Wake()
	}
}
func (w *MemberHandoverWorker) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		if err := w.scan(ctx); err != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "member permission worker scan failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
		case <-ticker.C:
		}
	}
}

type handoverCandidate struct{ ws, source, member, actor string }

func (w *MemberHandoverWorker) scan(ctx context.Context) error {
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := w.h.TxStarter.Begin(readCtx)
	if err != nil {
		return err
	}
	defer tx.Rollback(readCtx)
	rows, err := tx.Query(readCtx, `SELECT a.workspace_id::text,a.source_user_id::text,m.id::text
 FROM member_handover_automation a JOIN member m ON m.workspace_id=a.workspace_id AND m.user_id=a.source_user_id
 WHERE a.eligible AND (a.attempted_at IS NULL OR a.attempted_at<now()-interval '5 minutes')
 ORDER BY a.attempted_at NULLS FIRST,a.workspace_id,a.source_user_id LIMIT 100`)
	if err != nil {
		return err
	}
	var candidates []handoverCandidate
	for rows.Next() {
		var c handoverCandidate
		if err = rows.Scan(&c.ws, &c.source, &c.member); err != nil {
			break
		}
		candidates = append(candidates, c)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	owners := map[string]string{}
	for _, c := range candidates {
		if _, ok := owners[c.ws]; ok {
			continue
		}
		owners[c.ws] = ""
		rows, err = tx.Query(readCtx, `SELECT u.id::text,u.email FROM member m JOIN "user" u ON u.id=m.user_id
   WHERE m.workspace_id=$1 AND m.role='owner' AND u.account_disabled_at IS NULL ORDER BY m.created_at,m.id`, c.ws)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, email string
			if err = rows.Scan(&id, &email); err != nil {
				break
			}
			if owners[c.ws] == "" && !auth.IsTemporarilyDisabledUser(id, email) {
				owners[c.ws] = id
			}
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return err
		}
	}
	tx.Rollback(readCtx)
	// At most two employee transactions run concurrently; no directory lock or
	// directory scheduler goroutine is held while permissions/messages are handled.
	jobs := make(chan handoverCandidate)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range jobs {
				if ctx.Err() != nil {
					continue
				}
				if c.actor == "" {
					if err := w.h.recordAutomaticHandoverProblem(ctx, c.ws, c.source, c.member, "", "pending", "people_unavailable"); err != nil {
						slog.WarnContext(ctx, "member permission authority missing", "error", err)
					}
					continue
				}
				attempt, cancel := context.WithTimeout(ctx, 20*time.Second)
				status, reason, err := w.h.runAutomaticMemberHandover(attempt, c.ws, c.actor, c.member)
				cancel()
				if err != nil {
					status, reason = "failed", "execution_failed"
					slog.WarnContext(ctx, "member permission opening failed", "workspace_id", c.ws, "source_user_id", c.source, "error", err)
				}
				if status == "pending" || status == "failed" {
					if err := w.h.recordAutomaticHandoverProblem(ctx, c.ws, c.source, c.member, c.actor, status, reason); err != nil {
						slog.WarnContext(ctx, "member permission outcome persistence failed", "error", err)
					}
				}
			}
		}()
	}
	for _, c := range candidates {
		c.actor = owners[c.ws]
		select {
		case jobs <- c:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return ctx.Err()
		}
	}
	close(jobs)
	wg.Wait()
	return nil
}
