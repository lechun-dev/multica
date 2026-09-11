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
	"github.com/multica-ai/multica/server/internal/integrations/dingtalkpersonal"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/redact"
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
	personalWakeup     dingtalkPersonalMessageWakeup
}

type dingtalkPersonalMessageWakeup interface {
	NotifyDingTalkPersonalMessageAvailable(userID string)
}

const (
	dingtalkNotifyWorkerRetryMin = time.Second
	dingtalkNotifyWorkerRetryMax = 30 * time.Second
)

// registerDingTalkNotifyRuntime wires the personal-mention outbox to the
// server-side DWS sender. The existing robot worker remains independently
// gated, so personal delivery cannot alter its routing or payloads.
func registerDingTalkNotifyRuntime(bus *events.Bus, pool *pgxpool.Pool) (*dingtalkpersonal.Service, error) {
	if bus == nil || pool == nil {
		slog.Warn("dingtalk notify disabled: event bus or database is unavailable")
		return nil, &dingtalkpersonal.DeliveryError{Code: "dws_runtime_unavailable", Message: "服务器钉钉个人消息运行环境暂时不可用。"}
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
		return nil, &dingtalkpersonal.DeliveryError{Code: "dws_database_unavailable", Message: "服务器暂时无法初始化钉钉个人消息数据库。", Cause: err}
	}

	var personalService *dingtalkpersonal.Service
	var personalInitErr error
	if dwsKey, keyErr := secretbox.LoadKey("MULTICA_DWS_TOKEN_KEY"); keyErr != nil {
		slog.Warn("dingtalk server personal delivery unavailable", "error", keyErr)
		code := "dws_encryption_key_invalid"
		message := "服务器钉钉个人消息加密密钥格式无效，请联系管理员。"
		if strings.TrimSpace(os.Getenv("MULTICA_DWS_TOKEN_KEY")) == "" {
			code = "dws_encryption_key_missing"
			message = "服务器尚未配置钉钉个人消息加密密钥，请联系管理员。"
		}
		personalInitErr = &dingtalkpersonal.DeliveryError{Code: code, Message: message, Cause: keyErr}
	} else if dwsBox, boxErr := secretbox.New(dwsKey); boxErr != nil {
		slog.Warn("dingtalk server personal delivery unavailable", "error", boxErr)
		personalInitErr = &dingtalkpersonal.DeliveryError{Code: "dws_encryption_key_invalid", Message: "服务器钉钉个人消息加密密钥格式无效，请联系管理员。", Cause: boxErr}
	} else {
		personalService, err = dingtalkpersonal.New(dingtalkpersonal.Config{
			Pool: pool, Box: dwsBox,
			ClientID:        strings.TrimSpace(os.Getenv("DINGTALK_CLIENT_ID")),
			ClientSecret:    strings.TrimSpace(os.Getenv("DINGTALK_CLIENT_SECRET")),
			CorpID:          strings.TrimSpace(os.Getenv("DINGTALK_CORP_ID")),
			TokenEndpoint:   strings.TrimSpace(os.Getenv("DINGTALK_OAUTH_TOKEN_URL")),
			ChatEndpoint:    strings.TrimSpace(os.Getenv("DINGTALK_DWS_CHAT_ENDPOINT")),
			ContactEndpoint: strings.TrimSpace(os.Getenv("DINGTALK_DWS_CONTACT_ENDPOINT")),
			IMEndpoint:      strings.TrimSpace(os.Getenv("DINGTALK_DWS_IM_ENDPOINT")),
		})
		if err != nil {
			slog.Warn("dingtalk server personal delivery unavailable", "error", err)
			personalInitErr = err
			personalService = nil
		} else {
			personalService.Start(context.Background())
			slog.Info("dingtalk server personal delivery enabled")
		}
	}

	runtime := &dingtalkNotifyRuntime{
		pool:         pool,
		agentOwner:   dingtalkAgentOwnerResolver(pool),
		agentDetails: dingtalkAgentDetailsResolver(pool),
	}
	runtime.personalWakeup = personalService
	bus.Subscribe(protocol.EventCommentCreated, runtime.handleComment)
	slog.Info("dingtalk personal mention outbox enabled")

	config := notify.ConfigFromEnv(os.Getenv)
	if missing := config.MissingNotificationSettings(); len(missing) > 0 {
		_ = sqlDB.Close()
		slog.Info("dingtalk robot notify disabled: application configuration is incomplete", "missing", strings.Join(missing, ","))
		return personalService, personalInitErr
	}

	store := &notify.SQLStore{DB: sqlDB, Lease: 2 * time.Minute}
	provider := loggingDingTalkNotifyProvider{next: &notify.DingTalkProvider{
		BaseURL:      strings.TrimSpace(config.DingTalkAPIBaseURL),
		ClientID:     strings.TrimSpace(config.DingTalkClientID),
		ClientSecret: strings.TrimSpace(config.DingTalkClientSecret),
		RobotCode:    strings.TrimSpace(config.DingTalkRobotCode),
	}}
	runtime.store = store
	runtime.resolver = dingtalkMentionResolver{pool: pool}
	runtime.provider = provider
	runtime.audit = notify.SQLAuditSink{DB: sqlDB}
	runtime.workerInterval = config.WorkerInterval
	runtime.maxAttempts = config.MaxAttempts
	runtime.agentOwnerMentions = config.AgentOwnerMentions
	bus.Subscribe(protocol.EventTaskCompleted, runtime.handleTaskCompleted)
	go runtime.run(context.Background())
	slog.Info("dingtalk member notifications enabled")
	return personalService, personalInitErr
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
	resultText := ""
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
		if value, ok := payload["output"].(string); ok {
			resultText = strings.TrimSpace(value)
		}
	}
	if taskID == "" || agentID == "" {
		return
	}
	resultText = r.loadTaskCompletionText(context.Background(), e.WorkspaceID, taskID, resultText)
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
	completionContext := r.loadTaskNotificationContext(context.Background(), e)
	recipients := []string{ownerID, initiatorID}
	if strings.TrimSpace(ownerID) == "" && strings.TrimSpace(initiatorID) == "" {
		slog.Info("dingtalk notify: completed Agent has no human recipients", "workspace_id", e.WorkspaceID, "agent_id", agentID, "task_id", taskID)
		return
	}
	event := notify.AgentCompleted{EventID: taskID, WorkspaceID: e.WorkspaceID, AgentID: agentID, AgentName: agentName, ResultText: resultText, CompletedAt: time.Now().UTC()}
	if completionContext != nil {
		event.WorkspaceName = completionContext.workspaceName
		event.ProjectName = completionContext.projectName
		event.IssueIdentifier = completionContext.issueIdentifier
		event.IssueTitle = completionContext.issueTitle
		event.SourceURL = completionContext.sourceURL
	}
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

// loadTaskCompletionText mirrors the content visible in MissionOS whenever
// the Agent posted a task-linked comment. The terminal output carried by the
// event is the fallback for tool-only runs and older comments without lineage.
func (r *dingtalkNotifyRuntime) loadTaskCompletionText(ctx context.Context, workspaceID, taskID, fallback string) string {
	if r == nil || r.pool == nil || strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(taskID) == "" {
		return strings.TrimSpace(fallback)
	}
	var content string
	err := r.pool.QueryRow(ctx, `
		SELECT content
		FROM comment
		WHERE workspace_id = $1
		  AND source_task_id = $2
		  AND author_type = 'agent'
		ORDER BY created_at DESC, id DESC
		LIMIT 1`, workspaceID, taskID).Scan(&content)
	if err != nil {
		return strings.TrimSpace(fallback)
	}
	return strings.TrimSpace(redact.Text(content))
}

// loadTaskNotificationContext builds best-effort source/task metadata and a
// link for the completed issue or chat. Delivery remains valid when the row
// has already been removed or the public application URL is not configured.
func (r *dingtalkNotifyRuntime) loadTaskNotificationContext(ctx context.Context, e events.Event) *dingtalkMentionContext {
	if r == nil || r.pool == nil || strings.TrimSpace(e.WorkspaceID) == "" {
		return nil
	}
	appURL := appURLFromEnv()
	payload, _ := e.Payload.(map[string]any)
	issueID, _ := payload["issue_id"].(string)
	chatSessionID, _ := payload["chat_session_id"].(string)
	out := &dingtalkMentionContext{}
	if strings.TrimSpace(issueID) != "" {
		var slug, prefix string
		var number int32
		if err := r.pool.QueryRow(ctx, `
			SELECT w.name, w.slug, w.issue_prefix, i.number, i.title, COALESCE(p.title, '')
			FROM issue i
			JOIN workspace w ON w.id = i.workspace_id
			LEFT JOIN project p ON p.id = i.project_id AND p.workspace_id = i.workspace_id
			WHERE i.id = $1 AND i.workspace_id = $2`, issueID, e.WorkspaceID).
			Scan(&out.workspaceName, &slug, &prefix, &number, &out.issueTitle, &out.projectName); err == nil {
			out.issueIdentifier = strings.TrimSpace(prefix) + "-" + strconv.Itoa(int(number))
			if out.issueIdentifier == "-0" {
				out.issueIdentifier = issueID
			}
			if appURL != "" {
				segment := strings.TrimSpace(slug)
				if segment == "" {
					segment = e.WorkspaceID
				}
				out.sourceURL = strings.TrimRight(appURL, "/") + "/" + url.PathEscape(segment) + "/issues/" + url.PathEscape(out.issueIdentifier)
			}
			return out
		}
	}
	if strings.TrimSpace(chatSessionID) != "" {
		var slug string
		if err := r.pool.QueryRow(ctx, `
			SELECT w.name, w.slug, COALESCE(p.title, ''), COALESCE(cs.title, '')
			FROM chat_session cs
			JOIN workspace w ON w.id = cs.workspace_id
			LEFT JOIN project p ON p.id = cs.project_id AND p.workspace_id = cs.workspace_id
			WHERE cs.id = $1 AND cs.workspace_id = $2`, chatSessionID, e.WorkspaceID).
			Scan(&out.workspaceName, &slug, &out.projectName, &out.issueTitle); err == nil {
			if strings.TrimSpace(out.issueTitle) == "" {
				out.issueTitle = "智能体对话任务"
			}
			if appURL != "" {
				segment := strings.TrimSpace(slug)
				if segment == "" {
					segment = e.WorkspaceID
				}
				out.sourceURL = strings.TrimRight(appURL, "/") + "/" + url.PathEscape(segment) + "/chat?session=" + url.QueryEscape(chatSessionID)
			}
			return out
		}
	}
	return nil
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
	r.enqueuePersonalMentions(e.WorkspaceID, commentID, issueID, content, actorType, actorID, mentions)
	if r.store == nil || r.resolver == nil {
		return
	}
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

func (r *dingtalkNotifyRuntime) enqueuePersonalMentions(workspaceID, commentID, issueID, content, actorType, actorID string, mentions []util.Mention) {
	if r == nil || r.pool == nil || strings.TrimSpace(actorID) == "" {
		return
	}
	senderUserID := r.personalMentionSenderUserID(workspaceID, actorType, actorID)
	if senderUserID == "" {
		if actorType == "agent" {
			slog.Info("dingtalk personal mention: Agent owner unavailable", "workspace_id", workspaceID, "agent_id", actorID)
		}
		return
	}
	var enabled bool
	if err := r.pool.QueryRow(context.Background(), `
		SELECT COALESCE(
		    (SELECT preferences->>'dingtalk_personal_mentions'
		     FROM notification_preference
		     WHERE workspace_id = $1 AND user_id = $2),
		    'all'
		) <> 'muted'`, workspaceID, senderUserID).Scan(&enabled); err != nil || !enabled {
		if err != nil {
			slog.Warn("dingtalk personal mention: preference lookup failed", "workspace_id", workspaceID, "error", err)
		}
		return
	}

	contextData := r.loadMentionContext(context.Background(), issueID, workspaceID, actorType, actorID, commentID)
	event := notify.MentionCreated{
		EventID: commentID, WorkspaceID: workspaceID,
		Actor: notify.Actor{ID: actorID, Kind: actorType},
		Text:  content, CreatedAt: time.Now().UTC(),
	}
	if contextData != nil {
		event.WorkspaceName = contextData.workspaceName
		event.ProjectName = contextData.projectName
		event.IssueIdentifier = contextData.issueIdentifier
		event.IssueTitle = contextData.issueTitle
		event.SourceURL = contextData.sourceURL
		event.Actor.Name = contextData.actorName
	}
	markdown := notify.FormatPersonalMentionText(event)
	seen := make(map[string]struct{}, len(mentions))
	enqueued := 0
	for _, mention := range mentions {
		targetID := strings.TrimSpace(mention.ID)
		if mention.Type != "member" || targetID == "" {
			continue
		}
		if _, exists := seen[targetID]; exists {
			continue
		}
		seen[targetID] = struct{}{}
		result, err := r.pool.Exec(context.Background(), `
			INSERT INTO dingtalk_personal_message (
			    workspace_id, comment_id, sender_user_id, sender_ding_user_id,
			    sender_union_id, sender_corp_id, recipient_user_id,
			    recipient_ding_user_id, markdown, idempotency_key
			)
			SELECT $1::uuid, $2::uuid, $3::uuid, sender.ding_user_id,
			       NULLIF(sender.union_id, ''), NULLIF($6, ''), $4::uuid,
			       recipient.ding_user_id, $5,
			       'dingtalk-personal-mention:' || $2::text || ':' || $4::text
			FROM LATERAL (
			    SELECT COALESCE(ding_user_id, '') AS ding_user_id,
			           COALESCE(union_id, '') AS union_id
			    FROM dingtalk_notify_identities
			    WHERE multica_user_id = $3::text AND active = true
			      AND (COALESCE(union_id, '') <> '' OR COALESCE(ding_user_id, '') <> '')
			    ORDER BY updated_at DESC
			    LIMIT 1
			) sender
			CROSS JOIN LATERAL (
			    SELECT ding_user_id
			    FROM dingtalk_notify_identities
			    WHERE multica_user_id = $4::text AND active = true AND login_only = false
			      AND COALESCE(ding_user_id, '') <> ''
			    ORDER BY updated_at DESC
			    LIMIT 1
			) recipient
			WHERE EXISTS (
			    SELECT 1 FROM member
			    WHERE workspace_id = $1::uuid AND user_id = $3::uuid
			)
			  AND EXISTS (
			    SELECT 1 FROM member
			    WHERE workspace_id = $1::uuid AND user_id = $4::uuid
			)
			ON CONFLICT (idempotency_key) DO NOTHING`,
			workspaceID, commentID, senderUserID, targetID, markdown,
			strings.TrimSpace(os.Getenv("DINGTALK_CORP_ID")))
		if err != nil {
			slog.Warn("dingtalk personal mention: enqueue failed", "comment_id", commentID, "target_id", targetID, "error", err)
			continue
		}
		if result.RowsAffected() > 0 {
			enqueued++
			continue
		}
		reason, reasonErr := r.personalMentionEnqueueSkipReason(workspaceID, commentID, senderUserID, targetID)
		if reasonErr != nil {
			slog.Warn("dingtalk personal mention: enqueue produced no row and diagnosis failed", "comment_id", commentID, "target_id", targetID, "error", reasonErr)
			continue
		}
		if reason == "duplicate" {
			slog.Info("dingtalk personal mention: duplicate ignored", "comment_id", commentID, "target_id", targetID)
			continue
		}
		slog.Warn("dingtalk personal mention: enqueue skipped", "comment_id", commentID, "target_id", targetID, "reason", reason)
	}
	if enqueued == 0 {
		return
	}
	if r.personalWakeup != nil {
		r.personalWakeup.NotifyDingTalkPersonalMessageAvailable(senderUserID)
	}
	slog.Info("dingtalk personal mentions enqueued", "comment_id", commentID, "workspace_id", workspaceID, "actor_type", actorType, "actor_id", actorID, "sender_user_id", senderUserID, "target_count", enqueued)
}

func (r *dingtalkNotifyRuntime) personalMentionSenderUserID(workspaceID, actorType, actorID string) string {
	actorID = strings.TrimSpace(actorID)
	switch actorType {
	case "member":
		return actorID
	case "agent":
		return r.resolveAgentOwner(workspaceID, actorID)
	default:
		return ""
	}
}

func (r *dingtalkNotifyRuntime) personalMentionEnqueueSkipReason(workspaceID, commentID, senderUserID, recipientUserID string) (string, error) {
	var reason string
	err := r.pool.QueryRow(context.Background(), `
		SELECT CASE
		    WHEN EXISTS (
		        SELECT 1 FROM dingtalk_personal_message
		        WHERE idempotency_key = 'dingtalk-personal-mention:' || $2::text || ':' || $4::text
		    ) THEN 'duplicate'
		    WHEN NOT EXISTS (
		        SELECT 1 FROM member WHERE workspace_id = $1::uuid AND user_id = $3::uuid
		    ) THEN 'sender_not_in_workspace'
		    WHEN NOT EXISTS (
		        SELECT 1 FROM dingtalk_notify_identities
		        WHERE multica_user_id = $3::text AND active = true
		          AND (COALESCE(union_id, '') <> '' OR COALESCE(ding_user_id, '') <> '')
		    ) THEN 'sender_identity_unavailable'
		    WHEN NOT EXISTS (
		        SELECT 1 FROM member WHERE workspace_id = $1::uuid AND user_id = $4::uuid
		    ) THEN 'recipient_not_in_workspace'
		    WHEN NOT EXISTS (
		        SELECT 1 FROM dingtalk_notify_identities
		        WHERE multica_user_id = $4::text AND active = true
		    ) THEN 'recipient_identity_unavailable'
		    WHEN NOT EXISTS (
		        SELECT 1 FROM dingtalk_notify_identities
		        WHERE multica_user_id = $4::text AND active = true AND login_only = false
		          AND COALESCE(ding_user_id, '') <> ''
		    ) THEN 'recipient_identity_not_send_capable'
		    ELSE 'unknown'
		END`, workspaceID, commentID, senderUserID, recipientUserID).Scan(&reason)
	return reason, err
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
