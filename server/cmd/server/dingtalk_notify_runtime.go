package main

import (
	"context"
	"encoding/json"
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
	"github.com/multica-ai/multica/server/internal/integrations/dingtalk"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// dingtalkNotifyRuntime is the thin host bridge. All routing, idempotency,
// retry, and message formatting stay in the standalone extension; the host
// only maps Multica events/rows to its interfaces and reuses the built-in
// per-installation DingTalk sender.
type dingtalkNotifyRuntime struct {
	queries *db.Queries
	pool    *pgxpool.Pool
	decrypt dingtalk.Decrypter
	client  *dingtalk.Client
	store   notify.Store
}

// registerDingTalkNotifyRuntime wires member mentions only. Agent targets are
// intentionally left disabled by notify.BuildMessages' default options.
func registerDingTalkNotifyRuntime(bus *events.Bus, queries *db.Queries, pool *pgxpool.Pool, decrypt dingtalk.Decrypter, client *dingtalk.Client) {
	var store notify.Store
	if pool != nil {
		// The extension owns its table names and migration; this separate
		// database/sql handle keeps the host's pgx pool contract unchanged while
		// allowing the durable extension SQLStore to lease rows across replicas.
		sqlDB := stdlib.OpenDB(*pool.Config().ConnConfig)
		store = &notify.SQLStore{DB: sqlDB, Lease: 2 * time.Minute}
	} else {
		store = notify.NewMemoryStore()
	}
	runtime := &dingtalkNotifyRuntime{queries: queries, pool: pool, decrypt: decrypt, client: client, store: store}
	bus.Subscribe(protocol.EventCommentCreated, runtime.handleComment)
	go runtime.run(context.Background())
}

func (r *dingtalkNotifyRuntime) run(ctx context.Context) {
	worker := notify.Worker{Store: r.store, Provider: dingtalkMemberProvider{queries: r.queries, decrypt: r.decrypt, client: r.client}}
	if err := worker.Run(ctx, 2*time.Second, 25); err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn("dingtalk notify worker stopped", "error", err)
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
	messages, failures, err := notify.BuildMessages(context.Background(), event, dingtalkMentionResolver{queries: r.queries, pool: r.pool})
	if err != nil {
		slog.Warn("dingtalk notify: route mention failed", "error", err)
		return
	}
	for _, failure := range failures {
		slog.Info("dingtalk notify: target skipped", "target_id", failure.Message.TargetID, "target_kind", failure.Message.TargetKind, "status", failure.Status, "reason", failure.Error)
	}
	if err := notify.EnqueueMessages(context.Background(), r.store, messages, time.Now().UTC()); err != nil {
		slog.Warn("dingtalk notify: enqueue failed", "error", err)
	}
}

type dingtalkMentionResolver struct {
	queries *db.Queries
	pool    *pgxpool.Pool
}

func (r dingtalkMentionResolver) MemberBinding(ctx context.Context, workspaceID, memberID string) (notify.MemberBinding, bool, error) {
	if r.pool == nil {
		return notify.MemberBinding{}, false, errors.New("DingTalk member resolver database is unavailable")
	}
	var dingUserID string
	if err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(ding_user_id, '')
		FROM dingtalk_notify_identities
		WHERE multica_user_id = $1 AND active = true AND login_only = false
		ORDER BY updated_at DESC
		LIMIT 1`, memberID).Scan(&dingUserID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return notify.MemberBinding{}, false, nil
		}
		return notify.MemberBinding{}, false, err
	}
	var config []byte
	var status string
	if err := r.pool.QueryRow(ctx, `
		SELECT id, config, status
		FROM channel_installation
		WHERE workspace_id = $1
		  AND channel_type = 'dingtalk'
		  AND status = 'active'
		  AND config ->> 'app_id' = $2
		ORDER BY updated_at DESC
		LIMIT 1`, workspaceID, strings.TrimSpace(os.Getenv("DINGTALK_CLIENT_ID"))).Scan(&config, &status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return notify.MemberBinding{}, false, nil
		}
		return notify.MemberBinding{}, false, err
	}
	var cfg struct {
		RobotCode string `json:"robot_code"`
		AppID     string `json:"app_id"`
	}
	_ = json.Unmarshal(config, &cfg)
	if cfg.RobotCode == "" {
		cfg.RobotCode = cfg.AppID
	}
	return notify.MemberBinding{WorkspaceID: workspaceID, MemberID: memberID, DingUserID: dingUserID, RobotCode: cfg.RobotCode, Active: status == "active" && cfg.RobotCode != ""}, true, nil
}

func (r dingtalkMentionResolver) AgentChannels(context.Context, string, string) ([]notify.AgentChannel, error) {
	return nil, nil
}

type dingtalkMemberProvider struct {
	queries *db.Queries
	decrypt dingtalk.Decrypter
	client  *dingtalk.Client
}

func (p dingtalkMemberProvider) Send(ctx context.Context, message notify.Message) error {
	if message.TargetKind != "member" || message.ChannelType != "p2p" || message.DingUserID == "" || message.RobotCode == "" {
		return errors.New("dingtalk member destination is incomplete")
	}
	inst, err := p.queries.GetChannelInstallationByAppID(ctx, db.GetChannelInstallationByAppIDParams{ChannelType: string(dingtalk.TypeDingTalk), AppID: message.RobotCode})
	if err != nil {
		return err
	}
	return dingtalk.SendP2PFromInstallation(ctx, p.queries, p.decrypt, p.client, inst.ID, message.DingUserID, message.Text)
}
