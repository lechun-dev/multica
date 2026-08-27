package main

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"os"
	"strconv"
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
	pool           *pgxpool.Pool
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
		pool:           pool,
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
	var commentID, issueID, content, actorType, actorID string
	switch c := payload["comment"].(type) {
	case handler.CommentResponse:
		commentID, issueID, content, actorType = c.ID, c.IssueID, c.Content, c.AuthorType
		actorID = c.AuthorID
	case map[string]any:
		commentID, _ = c["id"].(string)
		issueID, _ = c["issue_id"].(string)
		content, _ = c["content"].(string)
		actorType, _ = c["author_type"].(string)
		actorID, _ = c["author_id"].(string)
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
	if actorType == "" {
		actorType = e.ActorType
	}
	if actorID == "" {
		actorID = e.ActorID
	}
	mention := notify.CommentMention{
		EventID: commentID, WorkspaceID: e.WorkspaceID,
		Actor:   notify.Actor{ID: actorID, Kind: actorType},
		Targets: targets, Body: content, CreatedAt: time.Now().UTC(),
	}
	if context := r.loadMentionContext(context.Background(), issueID, e.WorkspaceID, actorType, actorID, commentID); context != nil {
		mention.WorkspaceName = context.workspaceName
		mention.ProjectName = context.projectName
		mention.IssueIdentifier = context.issueIdentifier
		mention.IssueTitle = context.issueTitle
		mention.SourceURL = context.sourceURL
		mention.Actor.Name = context.actorName
	}
	event, err := notify.AdaptCommentMention(mention)
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

type dingtalkMentionContext struct {
	workspaceName   string
	projectName     string
	issueIdentifier string
	issueTitle      string
	sourceURL       string
	actorName       string
}

// loadMentionContext is best-effort enrichment. Notification delivery must
// still proceed with the original comment when a context lookup is unavailable.
func (r *dingtalkNotifyRuntime) loadMentionContext(ctx context.Context, issueID, workspaceID, actorType, actorID, commentID string) *dingtalkMentionContext {
	if r == nil || r.pool == nil || issueID == "" || workspaceID == "" {
		return nil
	}
	var out dingtalkMentionContext
	var slug, prefix string
	var number int32
	if err := r.pool.QueryRow(ctx, `
		SELECT w.name, w.slug, w.issue_prefix, i.number, i.title, COALESCE(p.title, '')
		FROM issue i
		JOIN workspace w ON w.id = i.workspace_id
		LEFT JOIN project p ON p.id = i.project_id AND p.workspace_id = i.workspace_id
		WHERE i.id = $1 AND i.workspace_id = $2`, issueID, workspaceID).
		Scan(&out.workspaceName, &slug, &prefix, &number, &out.issueTitle, &out.projectName); err != nil {
		slog.Warn("dingtalk notify: context lookup failed", "issue_id", issueID, "workspace_id", workspaceID, "error", err)
		return nil
	}
	out.issueIdentifier = strings.TrimSpace(prefix) + "-" + strconv.Itoa(int(number))
	if appURL := appURLFromEnv(); appURL != "" {
		segment := slug
		if segment == "" {
			segment = workspaceID
		}
		identifier := out.issueIdentifier
		if identifier == "-0" {
			identifier = issueID
		}
		out.sourceURL = strings.TrimRight(appURL, "/") + "/" + url.PathEscape(segment) + "/issues/" + url.PathEscape(identifier)
		if commentID != "" {
			out.sourceURL += "#comment-" + url.PathEscape(commentID)
		}
	}
	if actorID != "" {
		if actorType == "agent" {
			_ = r.pool.QueryRow(ctx, `SELECT name FROM agent WHERE id = $1 AND workspace_id = $2`, actorID, workspaceID).Scan(&out.actorName)
		} else {
			_ = r.pool.QueryRow(ctx, `
				SELECT u.name FROM "user" u JOIN member m ON m.user_id = u.id
				WHERE u.id = $1 AND m.workspace_id = $2 LIMIT 1`, actorID, workspaceID).Scan(&out.actorName)
		}
	}
	return &out
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
