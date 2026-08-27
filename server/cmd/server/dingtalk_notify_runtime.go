package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	notify "github.com/lechun-dev/multica/extensions/dingtalk-notify"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// dingtalkNotifyRuntime is the thin host bridge. All routing, idempotency,
// retry, and message formatting stay in the standalone extension; the host
// only maps Multica events/rows to its interfaces. Member P2P notifications use
// the deployment-wide login application and stay independent from Multica's
// existing per-Agent BYO robot integration.
type dingtalkNotifyRuntime struct {
	store          notify.Store
	resolver       notify.Resolver
	provider       notify.Provider
	audit          notify.AuditSink
	workerInterval time.Duration
	maxAttempts    int
}

const (
	dingtalkNotifyWorkerRetryMin = time.Second
	dingtalkNotifyWorkerRetryMax = 30 * time.Second
)

// registerDingTalkNotifyRuntime wires member mentions only. Agent targets are
// intentionally left disabled by notify.BuildMessages' default options. The
// runtime starts automatically once the global DingTalk application credentials
// are complete; no feature flag or BYO robot encryption key is involved.
func registerDingTalkNotifyRuntime(bus *events.Bus, pool *pgxpool.Pool) {
	config := notify.ConfigFromEnv(os.Getenv)
	if missing := config.MissingNotificationSettings(); len(missing) > 0 {
		slog.Info("dingtalk notify disabled: application configuration is incomplete", "missing", strings.Join(missing, ","))
		return
	}
	if bus == nil || pool == nil {
		slog.Warn("dingtalk notify disabled: event bus or database is unavailable")
		return
	}

	// The extension owns its schema and outbox. Use simple protocol because its
	// first migration contains multiple statements, matching the OAuth schema
	// bootstrap path.
	connConfig := *pool.Config().ConnConfig
	connConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	sqlDB := stdlib.OpenDB(connConfig)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	err := notify.EnsureSchema(ctx, sqlDB)
	cancel()
	if err != nil {
		_ = sqlDB.Close()
		slog.Warn("dingtalk notify disabled: schema initialization failed", "error", err)
		return
	}

	store := &notify.SQLStore{DB: sqlDB, Lease: 2 * time.Minute}
	provider := loggingDingTalkNotifyProvider{next: &notify.DingTalkProvider{
		BaseURL:      strings.TrimSpace(config.DingTalkAPIBaseURL),
		ClientID:     strings.TrimSpace(config.DingTalkClientID),
		ClientSecret: strings.TrimSpace(config.DingTalkClientSecret),
		RobotCode:    strings.TrimSpace(config.DingTalkRobotCode),
	}}
	runtime := &dingtalkNotifyRuntime{
		store:          store,
		resolver:       dingtalkMentionResolver{pool: pool},
		provider:       provider,
		audit:          notify.SQLAuditSink{DB: sqlDB},
		workerInterval: config.WorkerInterval,
		maxAttempts:    config.MaxAttempts,
	}
	bus.Subscribe(protocol.EventCommentCreated, runtime.handleComment)
	go runtime.run(context.Background())
	slog.Info("dingtalk member notifications enabled")
}

func (r *dingtalkNotifyRuntime) run(ctx context.Context) {
	interval := r.workerInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	worker := notify.Worker{
		Store:    r.store,
		Provider: r.provider,
		Policy:   notify.RetryPolicy{MaxAttempts: r.maxAttempts},
		Audit:    r.audit,
	}
	superviseDingTalkNotifyWorker(ctx, dingtalkNotifyWorkerRetryMin, dingtalkNotifyWorkerRetryMax,
		func(runCtx context.Context) error {
			return worker.Run(runCtx, interval, 25)
		})
}

func superviseDingTalkNotifyWorker(ctx context.Context, minDelay, maxDelay time.Duration, run func(context.Context) error) {
	if run == nil {
		return
	}
	if minDelay <= 0 {
		minDelay = time.Second
	}
	if maxDelay < minDelay {
		maxDelay = minDelay
	}
	delay := minDelay
	for {
		err := run(ctx)
		if err == nil || errors.Is(err, context.Canceled) || ctx.Err() != nil {
			return
		}
		slog.Warn("dingtalk notify worker interrupted; retrying", "error", err, "retry_in", delay)

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-timer.C:
		}
		if delay < maxDelay {
			delay *= 2
			if delay > maxDelay {
				delay = maxDelay
			}
		}
	}
}

func (r *dingtalkNotifyRuntime) handleComment(e events.Event) {
	payload, ok := e.Payload.(map[string]any)
	if !ok {
		return
	}
	var commentID, content, actorType string
	switch c := payload["comment"].(type) {
	case handler.CommentResponse:
		commentID, content, actorType = c.ID, c.Content, c.AuthorType
	case map[string]any:
		commentID, _ = c["id"].(string)
		content, _ = c["content"].(string)
		actorType, _ = c["author_type"].(string)
	default:
		return
	}
	if commentID == "" || content == "" || actorType == "system" {
		return
	}
	mentions := util.ParseMentions(content)
	targets := make([]notify.MentionTarget, 0, len(mentions))
	for _, mention := range mentions {
		if mention.Type == "member" {
			targets = append(targets, notify.MentionTarget{ID: mention.ID, Kind: "member"})
		}
	}
	if len(targets) == 0 {
		return
	}
	event, err := notify.AdaptCommentMention(notify.CommentMention{
		EventID: commentID, WorkspaceID: e.WorkspaceID,
		Actor:   notify.Actor{ID: e.ActorID, Kind: e.ActorType},
		Targets: targets, Body: content, CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		slog.Warn("dingtalk notify: invalid comment event", "error", err)
		return
	}
	messages, failures, err := notify.BuildMessages(context.Background(), event, r.resolver)
	if err != nil {
		slog.Warn("dingtalk notify: route mention failed", "event_id", commentID, "workspace_id", e.WorkspaceID, "error", err)
		return
	}
	for _, failure := range failures {
		slog.Info("dingtalk notify: target skipped", "target_id", failure.Message.TargetID, "target_kind", failure.Message.TargetKind, "status", failure.Status, "reason", failure.Error)
	}
	if err := notify.EnqueueMessages(context.Background(), r.store, messages, time.Now().UTC()); err != nil {
		slog.Warn("dingtalk notify: enqueue failed", "event_id", commentID, "workspace_id", e.WorkspaceID, "target_count", len(messages), "error", err)
		return
	}
	slog.Info("dingtalk notify: member mentions enqueued", "event_id", commentID, "workspace_id", e.WorkspaceID, "target_count", len(messages))
}

type dingtalkMentionResolver struct {
	pool *pgxpool.Pool
}

func (r dingtalkMentionResolver) MemberBinding(ctx context.Context, workspaceID, memberID string) (notify.MemberBinding, bool, error) {
	if r.pool == nil {
		return notify.MemberBinding{}, false, errors.New("DingTalk member resolver database is unavailable")
	}
	var dingUserID string
	if err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(ding_user_id, '')
		FROM dingtalk_notify_identities
		WHERE multica_user_id = $1
		  AND active = true
		  AND login_only = false
		  AND COALESCE(ding_user_id, '') <> ''
		ORDER BY updated_at DESC
		LIMIT 1`, memberID).Scan(&dingUserID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return notify.MemberBinding{}, false, nil
		}
		return notify.MemberBinding{}, false, err
	}
	return notify.MemberBinding{WorkspaceID: workspaceID, MemberID: memberID, DingUserID: dingUserID, Active: true}, true, nil
}

func (dingtalkMentionResolver) AgentChannels(context.Context, string, string) ([]notify.AgentChannel, error) {
	return nil, nil
}

type loggingDingTalkNotifyProvider struct{ next notify.Provider }

func (p loggingDingTalkNotifyProvider) Send(ctx context.Context, message notify.Message) error {
	if p.next == nil {
		return errors.New("dingtalk notify provider is unavailable")
	}
	err := p.next.Send(ctx, message)
	if err != nil {
		slog.Warn("dingtalk notify: delivery failed", "event_id", message.EventID, "workspace_id", message.WorkspaceID, "target_id", message.TargetID, "error", err)
		return err
	}
	slog.Info("dingtalk notify: delivered", "event_id", message.EventID, "workspace_id", message.WorkspaceID, "target_id", message.TargetID)
	return nil
}
