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
	// agentOwner resolves a user Agent's owner. Keeping this callback on the
	// host bridge avoids coupling the standalone notification module to the
	// Multica database model and makes the routing easy to test.
	agentOwner         func(context.Context, string, string) (string, error)
	agentDetails       func(context.Context, string, string) (string, string, error)
	agentOwnerMentions bool
}

const (
	dingtalkNotifyWorkerRetryMin = time.Second
	dingtalkNotifyWorkerRetryMax = 30 * time.Second
)

// registerDingTalkNotifyRuntime wires member mentions and, when enabled,
// explicit Agent mentions to the Agent owner's existing member binding. The
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
		store:              store,
		resolver:           dingtalkMentionResolver{pool: pool},
		provider:           provider,
		audit:              notify.SQLAuditSink{DB: sqlDB},
		pool:               pool,
		workerInterval:     config.WorkerInterval,
		maxAttempts:        config.MaxAttempts,
		agentOwner:         dingtalkAgentOwnerResolver(pool),
		agentDetails:       dingtalkAgentDetailsResolver(pool),
		agentOwnerMentions: config.AgentOwnerMentions,
	}
	bus.Subscribe(protocol.EventCommentCreated, runtime.handleComment)
	bus.Subscribe(protocol.EventTaskCompleted, runtime.handleTaskCompleted)
	go runtime.run(context.Background())
	slog.Info("dingtalk member notifications enabled")
}

// handleTaskCompleted sends a distinct completion message to both the Agent's
// owner and the human who actually initiated this task. The task event carries
// the initiator ID so delegated runs can notify the immediate caller rather
// than only the top-level originator. Duplicate IDs are collapsed by the
// extension, and unbound recipients are recorded as failed deliveries.
func (r *dingtalkNotifyRuntime) handleTaskCompleted(e events.Event) {
	if r == nil || r.store == nil || r.resolver == nil || strings.TrimSpace(e.WorkspaceID) == "" {
		return
	}
	taskID := strings.TrimSpace(e.TaskID)
	agentID := ""
	initiatorID := ""
	if payload, ok := e.Payload.(map[string]any); ok {
		if value, ok := payload["task_id"].(string); ok {
			taskID = strings.TrimSpace(value)
		}
		if value, ok := payload["agent_id"].(string); ok {
			agentID = strings.TrimSpace(value)
		}
		if value, ok := payload["initiator_user_id"].(string); ok {
			initiatorID = strings.TrimSpace(value)
		}
	}
	if taskID == "" || agentID == "" {
		return
	}
	ownerID, agentName := "", ""
	if r.agentDetails != nil {
		var err error
		ownerID, agentName, err = r.agentDetails(context.Background(), e.WorkspaceID, agentID)
		if err != nil {
			slog.Warn("dingtalk notify: resolve completed Agent details failed", "workspace_id", e.WorkspaceID, "agent_id", agentID, "error", err)
			return
		}
	} else {
		ownerID = r.resolveAgentOwner(e.WorkspaceID, agentID)
	}
	sourceURL := r.loadTaskSourceURL(context.Background(), e)
	recipients := []string{ownerID, initiatorID}
	if strings.TrimSpace(ownerID) == "" && strings.TrimSpace(initiatorID) == "" {
		slog.Info("dingtalk notify: completed Agent has no human recipients", "workspace_id", e.WorkspaceID, "agent_id", agentID, "task_id", taskID)
		return
	}
	event := notify.AgentCompleted{EventID: taskID, WorkspaceID: e.WorkspaceID, AgentID: agentID, AgentName: agentName, SourceURL: sourceURL, CompletedAt: time.Now().UTC()}
	messages, failures, err := notify.BuildCompletionMessages(context.Background(), event, recipients, r.resolver)
	if err != nil {
		slog.Warn("dingtalk notify: route Agent completion failed", "task_id", taskID, "workspace_id", e.WorkspaceID, "error", err)
		return
	}
	for _, failure := range failures {
		slog.Info("dingtalk notify: completed Agent recipient skipped", "task_id", taskID, "target_id", failure.Message.TargetID, "status", failure.Status, "reason", failure.Error)
	}
	if len(messages) == 0 {
		return
	}
	if err := notify.EnqueueMessages(context.Background(), r.store, messages, time.Now().UTC()); err != nil {
		slog.Warn("dingtalk notify: enqueue Agent completion failed", "task_id", taskID, "workspace_id", e.WorkspaceID, "target_count", len(messages), "error", err)
		return
	}
	slog.Info("dingtalk notify: Agent completion enqueued", "task_id", taskID, "workspace_id", e.WorkspaceID, "target_count", len(messages))
}

// loadTaskSourceURL builds a best-effort link to the completed issue or chat.
// Delivery remains valid without it when the row has already been removed or
// the public application URL is not configured.
func (r *dingtalkNotifyRuntime) loadTaskSourceURL(ctx context.Context, e events.Event) string {
	if r == nil || r.pool == nil || strings.TrimSpace(e.WorkspaceID) == "" {
		return ""
	}
	appURL := appURLFromEnv()
	if appURL == "" {
		return ""
	}
	payload, _ := e.Payload.(map[string]any)
	issueID, _ := payload["issue_id"].(string)
	chatSessionID, _ := payload["chat_session_id"].(string)
	var slug string
	if err := r.pool.QueryRow(ctx, `SELECT slug FROM workspace WHERE id = $1`, e.WorkspaceID).Scan(&slug); err != nil {
		return ""
	}
	segment := strings.TrimSpace(slug)
	if segment == "" {
		segment = e.WorkspaceID
	}
	base := strings.TrimRight(appURL, "/") + "/" + url.PathEscape(segment)
	if strings.TrimSpace(issueID) != "" {
		var prefix string
		var number int32
		if err := r.pool.QueryRow(ctx, `SELECT w.issue_prefix, i.number FROM issue i JOIN workspace w ON w.id = i.workspace_id WHERE i.id = $1 AND i.workspace_id = $2`, issueID, e.WorkspaceID).Scan(&prefix, &number); err == nil {
			identifier := strings.TrimSpace(prefix) + "-" + strconv.Itoa(int(number))
			if identifier == "-0" {
				identifier = issueID
			}
			return base + "/issues/" + url.PathEscape(identifier)
		}
	}
	if strings.TrimSpace(chatSessionID) != "" {
		return base + "/chat?session=" + url.QueryEscape(chatSessionID)
	}
	return ""
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
	if actorType == "" {
		actorType = e.ActorType
	}
	if actorID == "" {
		actorID = e.ActorID
	}
	mentions := util.ParseMentions(content)
	targets := make([]notify.MentionTarget, 0, len(mentions))
	actorOwnerID := actorID
	if actorType == "agent" {
		actorOwnerID = r.resolveAgentOwner(e.WorkspaceID, actorID)
	}
	for _, mention := range mentions {
		switch mention.Type {
		case "member":
			targets = append(targets, notify.MentionTarget{ID: mention.ID, Kind: "member"})
		case "agent":
			if !r.agentOwnerMentions {
				continue
			}
			ownerID := r.resolveAgentOwner(e.WorkspaceID, mention.ID)
			if ownerID == "" {
				slog.Info("dingtalk notify: agent owner unavailable", "workspace_id", e.WorkspaceID)
				continue
			}
			if ownerID == actorOwnerID {
				// The owner is already the person (or Agent) performing the
				// work; do not notify them about their own Agent usage.
				continue
			}
			targets = append(targets, notify.MentionTarget{ID: ownerID, Kind: "member"})
		}
	}
	if len(targets) == 0 {
		return
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
	slog.Info("dingtalk notify: mentions enqueued", "event_id", commentID, "workspace_id", e.WorkspaceID, "target_count", len(messages))
}

func dingtalkAgentOwnerResolver(pool *pgxpool.Pool) func(context.Context, string, string) (string, error) {
	return func(ctx context.Context, workspaceID, agentID string) (string, error) {
		if pool == nil {
			return "", errors.New("DingTalk agent owner resolver database is unavailable")
		}
		var ownerID *string
		err := pool.QueryRow(ctx, `
			SELECT owner_id::text
			FROM agent
			WHERE id = $1 AND workspace_id = $2 AND kind = 'user'
			  AND archived_at IS NULL`, agentID, workspaceID).Scan(&ownerID)
		if errors.Is(err, pgx.ErrNoRows) || ownerID == nil {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(*ownerID), nil
	}
}

func dingtalkAgentDetailsResolver(pool *pgxpool.Pool) func(context.Context, string, string) (string, string, error) {
	return func(ctx context.Context, workspaceID, agentID string) (string, string, error) {
		if pool == nil {
			return "", "", errors.New("DingTalk agent details resolver database is unavailable")
		}
		var ownerID *string
		var name string
		err := pool.QueryRow(ctx, `
			SELECT owner_id::text, name
			FROM agent
			WHERE id = $1 AND workspace_id = $2 AND kind = 'user'
			  AND archived_at IS NULL`, agentID, workspaceID).Scan(&ownerID, &name)
		if errors.Is(err, pgx.ErrNoRows) || ownerID == nil {
			return "", strings.TrimSpace(name), nil
		}
		if err != nil {
			return "", "", err
		}
		return strings.TrimSpace(*ownerID), strings.TrimSpace(name), nil
	}
}

func (r *dingtalkNotifyRuntime) resolveAgentOwner(workspaceID, agentID string) string {
	if r == nil || r.agentOwner == nil || strings.TrimSpace(agentID) == "" {
		return ""
	}
	ownerID, err := r.agentOwner(context.Background(), workspaceID, agentID)
	if err != nil {
		slog.Warn("dingtalk notify: resolve agent owner failed", "workspace_id", workspaceID, "error", err)
		return ""
	}
	return strings.TrimSpace(ownerID)
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
