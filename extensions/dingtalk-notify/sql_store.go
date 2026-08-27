package notify

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SQLStore is the production Store implementation. The host owns the
// database driver and connection lifecycle; this module only requires a
// *sql.DB, while tests can keep using MemoryStore without a connection.
type SQLStore struct {
	DB          *sql.DB
	Lease       time.Duration
	TablePrefix string
}

func (s *SQLStore) table(name string) string {
	prefix := s.TablePrefix
	if prefix == "" {
		prefix = "dingtalk_notify_"
	}
	return prefix + name
}

func (s *SQLStore) validate() error {
	if s == nil || s.DB == nil {
		return errors.New("sql store database is required")
	}
	return nil
}

func (s *SQLStore) Enqueue(ctx context.Context, item OutboxItem) (bool, error) {
	if err := s.validate(); err != nil {
		return false, err
	}
	if item.IdempotencyKey == "" || item.Message.EventID == "" || item.Message.WorkspaceID == "" {
		return false, errors.New("outbox item idempotency key and message identity are required")
	}
	now := time.Now().UTC()
	if item.CreatedAt.IsZero() {
		item.CreatedAt = now
	}
	if item.UpdatedAt.IsZero() {
		item.UpdatedAt = item.CreatedAt
	}
	if item.NextAttemptAt.IsZero() {
		item.NextAttemptAt = now
	}
	result, err := s.DB.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s
		(idempotency_key, workspace_id, event_id, target_id, target_kind, channel_id,
		 robot_code, ding_user_id, channel_type, message_text, status, attempts,
		 next_attempt_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		ON CONFLICT (idempotency_key) DO NOTHING`, s.table("outbox")),
		item.IdempotencyKey, item.Message.WorkspaceID, item.Message.EventID,
		item.Message.TargetID, item.Message.TargetKind, nullable(item.Message.ChannelID),
		nullable(item.Message.RobotCode), nullable(item.Message.DingUserID), item.Message.ChannelType,
		item.Message.Text, StatusPending, item.Attempts, item.NextAttemptAt, item.CreatedAt, item.UpdatedAt)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows > 0, err
}

func (s *SQLStore) ClaimDue(ctx context.Context, now time.Time, limit int) ([]OutboxItem, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		return nil, nil
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	lease := s.Lease
	if lease <= 0 {
		lease = 2 * time.Minute
	}
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`
		SELECT idempotency_key, workspace_id, event_id, target_id, target_kind,
		       COALESCE(channel_id, ''), COALESCE(robot_code, ''), COALESCE(ding_user_id, ''),
		       channel_type, message_text, status, attempts, next_attempt_at,
		       created_at, updated_at
		FROM %s
		WHERE (status = $1 AND next_attempt_at <= $2)
		   OR (status = $3 AND lease_until IS NOT NULL AND lease_until <= $2)
		ORDER BY next_attempt_at, created_at
		FOR UPDATE SKIP LOCKED
		LIMIT $4`, s.table("outbox")), StatusPending, now, StatusProcessing, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]OutboxItem, 0, limit)
	for rows.Next() {
		var item OutboxItem
		var message Message
		if err := rows.Scan(&item.IdempotencyKey, &message.WorkspaceID, &message.EventID,
			&message.TargetID, &message.TargetKind, &message.ChannelID, &message.RobotCode,
			&message.DingUserID, &message.ChannelType, &message.Text, &item.Status,
			&item.Attempts, &item.NextAttemptAt, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		item.ID, item.Message = item.IdempotencyKey, message
		item.Attempts++
		item.Status, item.UpdatedAt = StatusProcessing, now
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET status=$1, attempts=$2, lease_until=$3, updated_at=$4 WHERE idempotency_key=$5`, s.table("outbox")), StatusProcessing, item.Attempts, now.Add(lease), now, item.IdempotencyKey); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *SQLStore) MarkDelivered(ctx context.Context, id string, at time.Time) error {
	return s.updateStatus(ctx, id, StatusDelivered, at, 0, "", nil)
}

func (s *SQLStore) MarkRetry(ctx context.Context, id string, next time.Time, attempts int, reason string) error {
	return s.updateStatus(ctx, id, StatusPending, time.Now().UTC(), attempts, reason, &next)
}

func (s *SQLStore) MarkFailed(ctx context.Context, id string, at time.Time, attempts int, reason string) error {
	return s.updateStatus(ctx, id, StatusFailed, at, attempts, reason, nil)
}

func (s *SQLStore) List(ctx context.Context, workspaceID string, limit int) ([]OutboxItem, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.DB.QueryContext(ctx, fmt.Sprintf(`
		SELECT idempotency_key, workspace_id, event_id, target_id, target_kind,
		       COALESCE(channel_id,''), COALESCE(robot_code,''), COALESCE(ding_user_id,''),
		       channel_type, message_text, status, attempts, next_attempt_at, created_at, updated_at
		FROM %s WHERE ($1 = '' OR workspace_id=$1) ORDER BY updated_at DESC LIMIT $2`, s.table("outbox")), workspaceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]OutboxItem, 0, limit)
	for rows.Next() {
		var item OutboxItem
		var message Message
		if err := rows.Scan(&item.IdempotencyKey, &message.WorkspaceID, &message.EventID, &message.TargetID, &message.TargetKind, &message.ChannelID, &message.RobotCode, &message.DingUserID, &message.ChannelType, &message.Text, &item.Status, &item.Attempts, &item.NextAttemptAt, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		item.ID, item.Message = item.IdempotencyKey, message
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLStore) updateStatus(ctx context.Context, id, status string, at time.Time, attempts int, reason string, next *time.Time) error {
	if err := s.validate(); err != nil {
		return err
	}
	var nextArg any
	if next != nil {
		nextArg = *next
	}
	args := []any{status, at, attempts, nullable(reason), nextArg, id}
	result, err := s.DB.ExecContext(ctx, fmt.Sprintf(`
		UPDATE %s SET status=$1, updated_at=$2,
		 attempts=CASE WHEN $3 > 0 THEN $3 ELSE attempts END,
		 last_error=$4, next_attempt_at=COALESCE($5, next_attempt_at), lease_until=NULL
		WHERE idempotency_key=$6`, s.table("outbox")), args...)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return fmt.Errorf("outbox item %q not found", id)
	}
	return nil
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

type SQLBindingStore struct {
	DB          *sql.DB
	TablePrefix string
}

func (s SQLBindingStore) table() string {
	prefix := s.TablePrefix
	if prefix == "" {
		prefix = "dingtalk_notify_"
	}
	return prefix + "member_bindings"
}
func (s SQLBindingStore) validate() error {
	if s.DB == nil {
		return errors.New("sql binding store database is required")
	}
	return nil
}

func (s SQLBindingStore) Upsert(ctx context.Context, binding MemberBinding) error {
	if err := s.validate(); err != nil {
		return err
	}
	if binding.WorkspaceID == "" || binding.MemberID == "" || binding.DingUserID == "" {
		return errors.New("binding identity is required")
	}
	_, err := s.DB.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (workspace_id, member_id, ding_user_id, union_id, open_id, active, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (workspace_id, member_id) DO UPDATE SET ding_user_id=$3, union_id=$4, open_id=$5, active=$6, updated_at=$7`, s.table()),
		binding.WorkspaceID, binding.MemberID, nullable(binding.DingUserID), nullable(binding.UnionID), nullable(binding.OpenID), binding.Active, time.Now().UTC())
	return err
}
func (s SQLBindingStore) Get(ctx context.Context, workspaceID, memberID string) (MemberBinding, bool, error) {
	if err := s.validate(); err != nil {
		return MemberBinding{}, false, err
	}
	var binding MemberBinding
	var unionID, openID sql.NullString
	err := s.DB.QueryRowContext(ctx, fmt.Sprintf(`SELECT workspace_id, member_id, COALESCE(ding_user_id, ''), union_id, open_id, active FROM %s WHERE workspace_id=$1 AND member_id=$2`, s.table()), workspaceID, memberID).Scan(&binding.WorkspaceID, &binding.MemberID, &binding.DingUserID, &unionID, &openID, &binding.Active)
	if errors.Is(err, sql.ErrNoRows) {
		return MemberBinding{}, false, nil
	}
	if err != nil {
		return MemberBinding{}, false, err
	}
	binding.UnionID, binding.OpenID = unionID.String, openID.String
	return binding, true, nil
}
func (s SQLBindingStore) Revoke(ctx context.Context, workspaceID, memberID string, at time.Time) error {
	if err := s.validate(); err != nil {
		return err
	}
	_, err := s.DB.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET active=false, updated_at=$3 WHERE workspace_id=$1 AND member_id=$2`, s.table()), workspaceID, memberID, at)
	return err
}

type SQLAgentChannelStore struct {
	DB          *sql.DB
	TablePrefix string
}

func (s SQLAgentChannelStore) table() string {
	prefix := s.TablePrefix
	if prefix == "" {
		prefix = "dingtalk_notify_"
	}
	return prefix + "agent_channels"
}
func (s SQLAgentChannelStore) validate() error {
	if s.DB == nil {
		return errors.New("sql channel store database is required")
	}
	return nil
}
func (s SQLAgentChannelStore) UpsertAgentChannel(ctx context.Context, channel AgentChannel) error {
	if err := s.validate(); err != nil {
		return err
	}
	_, err := s.DB.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (workspace_id, agent_id, channel_id, channel_name, robot_code, owner_id, active, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (workspace_id, agent_id, channel_id) DO UPDATE SET channel_name=$4, robot_code=$5, owner_id=$6, active=$7, updated_at=$8`, s.table()),
		channel.WorkspaceID, channel.AgentID, channel.ChannelID, channel.ChannelName, nullable(channel.RobotCode), nullable(channel.OwnerID), channel.Active, time.Now().UTC())
	return err
}
func (s SQLAgentChannelStore) ListAgentChannels(ctx context.Context, workspaceID, agentID string) ([]AgentChannel, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, fmt.Sprintf(`SELECT workspace_id, agent_id, channel_id, channel_name, COALESCE(robot_code,''), COALESCE(owner_id,''), active FROM %s WHERE workspace_id=$1 AND agent_id=$2 ORDER BY channel_name, channel_id`, s.table()), workspaceID, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	channels := make([]AgentChannel, 0)
	for rows.Next() {
		var channel AgentChannel
		if err := rows.Scan(&channel.WorkspaceID, &channel.AgentID, &channel.ChannelID, &channel.ChannelName, &channel.RobotCode, &channel.OwnerID, &channel.Active); err != nil {
			return nil, err
		}
		channels = append(channels, channel)
	}
	return channels, rows.Err()
}
func (s SQLAgentChannelStore) DeactivateAgentChannel(ctx context.Context, workspaceID, agentID, channelID string, at time.Time) error {
	if err := s.validate(); err != nil {
		return err
	}
	result, err := s.DB.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET active=false, updated_at=$4 WHERE workspace_id=$1 AND agent_id=$2 AND channel_id=$3`, s.table()), workspaceID, agentID, channelID, at)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return fmt.Errorf("agent channel %q not found", channelID)
	}
	return nil
}
func (s SQLAgentChannelStore) AgentChannels(ctx context.Context, workspaceID, agentID string) ([]AgentChannel, error) {
	return s.ListAgentChannels(ctx, workspaceID, agentID)
}

type SQLAuditSink struct {
	DB          *sql.DB
	TablePrefix string
}

func (s SQLAuditSink) outboxTable() string {
	prefix := s.TablePrefix
	if prefix == "" {
		prefix = "dingtalk_notify_"
	}
	return prefix + "outbox"
}
func (s SQLAuditSink) attemptsTable() string {
	prefix := s.TablePrefix
	if prefix == "" {
		prefix = "dingtalk_notify_"
	}
	return prefix + "delivery_attempts"
}
func (s SQLAuditSink) Record(ctx context.Context, audit DeliveryAudit) error {
	if s.DB == nil {
		return errors.New("sql audit sink database is required")
	}
	var outboxID int64
	if err := s.DB.QueryRowContext(ctx, fmt.Sprintf(`SELECT id FROM %s WHERE idempotency_key=$1`, s.outboxTable()), audit.OutboxID).Scan(&outboxID); err != nil {
		return err
	}
	_, err := s.DB.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (outbox_id, attempt_no, status, error_class, error_message, duration_ms, created_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`, s.attemptsTable()), outboxID, audit.Attempts, audit.Status, nullable(auditErrorClass(audit.Error)), nullable(audit.Error), audit.Duration.Milliseconds(), audit.At)
	return err
}

func auditErrorClass(message string) string {
	if message == "" {
		return ""
	}
	if strings.Contains(strings.ToLower(message), "429") {
		return "rate_limited"
	}
	if strings.Contains(strings.ToLower(message), "timeout") {
		return "timeout"
	}
	if strings.Contains(strings.ToLower(message), "401") || strings.Contains(strings.ToLower(message), "unauthorized") {
		return "unauthorized"
	}
	return "provider_error"
}
