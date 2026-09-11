// Package dingtalkpersonal delivers user-authored DingTalk direct messages
// from the Multica server. It intentionally does not shell out to the local
// DWS CLI: OAuth credentials are encrypted with a deployment-only key and the
// server calls the same DingTalk MCP contracts used by DWS.
package dingtalkpersonal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
)

const (
	defaultChatEndpoint    = "https://mcp-gw.dingtalk.com/server/0a1609437385696b77fc4771c3ddaf5656b487f809966c0cc8d4755e7b1d3b74"
	defaultContactEndpoint = "https://mcp-gw.dingtalk.com/server/db4b26cb38ea6a8739ad55d1997fa1da608cd36b33a6cf0f77884f70c49382fe"
	defaultIMEndpoint      = "https://mcp-gw.dingtalk.com/server/450eede6b54d83e030140e66ec77c98a2e89a0869ef4db481f8217a98a42f821"
	defaultTokenEndpoint   = "https://api.dingtalk.com/v1.0/oauth2/userAccessToken"
	workerLeaseOwner       = "server-dws-v1"
)

type OAuthCredential struct {
	AccessToken      string
	RefreshToken     string
	AccessExpiresAt  time.Time
	RefreshExpiresAt time.Time
	CorpID           string
	ClientID         string
}

type Status struct {
	Configured   bool   `json:"configured"`
	Connected    bool   `json:"connected"`
	State        string `json:"state"`
	Reason       string `json:"reason,omitempty"`
	Message      string `json:"message,omitempty"`
	PendingCount int64  `json:"pending_count"`
	ExpiresAt    string `json:"expires_at,omitempty"`
}

type Config struct {
	Pool            *pgxpool.Pool
	Box             *secretbox.Box
	HTTPClient      *http.Client
	ClientID        string
	ClientSecret    string
	CorpID          string
	TokenEndpoint   string
	ChatEndpoint    string
	ContactEndpoint string
	IMEndpoint      string
	WorkerInterval  time.Duration
	MaxAttempts     int
}

type Service struct {
	pool            *pgxpool.Pool
	box             *secretbox.Box
	httpClient      *http.Client
	clientID        string
	clientSecret    string
	corpID          string
	tokenEndpoint   string
	chatEndpoint    string
	contactEndpoint string
	imEndpoint      string
	workerInterval  time.Duration
	maxAttempts     int
	wake            chan struct{}
}

func New(config Config) (*Service, error) {
	if config.Pool == nil {
		return nil, &DeliveryError{Code: "dws_database_unavailable", Message: "服务器暂时无法访问钉钉个人消息数据库。"}
	}
	if config.Box == nil {
		return nil, &DeliveryError{Code: "dws_encryption_key_missing", Message: "服务器尚未配置钉钉个人消息加密密钥，请联系管理员。"}
	}
	if strings.TrimSpace(config.ClientID) == "" || strings.TrimSpace(config.ClientSecret) == "" {
		return nil, &DeliveryError{Code: "dws_oauth_not_configured", Message: "服务器尚未完整配置钉钉 OAuth Client ID 和 Client Secret，请联系管理员。"}
	}
	if strings.TrimSpace(config.CorpID) == "" {
		return nil, &DeliveryError{Code: "dws_organization_not_configured", Message: "服务器尚未配置第一阶段允许使用的钉钉组织，请联系管理员。"}
	}
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{
			Timeout: 35 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return errors.New("too many DingTalk redirects")
				}
				if len(via) > 0 && (req.URL.Scheme != via[0].URL.Scheme || !strings.EqualFold(req.URL.Host, via[0].URL.Host)) {
					req.Header.Del("Authorization")
					req.Header.Del("x-user-access-token")
				}
				return nil
			},
		}
	}
	if config.TokenEndpoint == "" {
		config.TokenEndpoint = defaultTokenEndpoint
	}
	if config.ChatEndpoint == "" {
		config.ChatEndpoint = defaultChatEndpoint
	}
	if config.ContactEndpoint == "" {
		config.ContactEndpoint = defaultContactEndpoint
	}
	if config.IMEndpoint == "" {
		config.IMEndpoint = defaultIMEndpoint
	}
	if config.WorkerInterval <= 0 {
		config.WorkerInterval = 5 * time.Second
	}
	if config.MaxAttempts <= 0 {
		config.MaxAttempts = 5
	}
	return &Service{
		pool: config.Pool, box: config.Box, httpClient: config.HTTPClient,
		clientID: strings.TrimSpace(config.ClientID), clientSecret: strings.TrimSpace(config.ClientSecret),
		corpID: strings.TrimSpace(config.CorpID), tokenEndpoint: config.TokenEndpoint,
		chatEndpoint: config.ChatEndpoint, contactEndpoint: config.ContactEndpoint, imEndpoint: config.IMEndpoint,
		workerInterval: config.WorkerInterval, maxAttempts: config.MaxAttempts,
		wake: make(chan struct{}, 1),
	}, nil
}

func (s *Service) Start(ctx context.Context) {
	if s == nil {
		return
	}
	go s.run(ctx)
}

func (s *Service) NotifyDingTalkPersonalMessageAvailable(string) {
	if s == nil {
		return
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) BindOAuthState(ctx context.Context, state, userID string, expiresAt time.Time) error {
	_, _ = s.pool.Exec(ctx, `DELETE FROM dingtalk_dws_oauth_state WHERE expires_at <= now()`)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO dingtalk_dws_oauth_state (state, multica_user_id, expires_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (state) DO UPDATE
		SET multica_user_id = EXCLUDED.multica_user_id, expires_at = EXCLUDED.expires_at`,
		strings.TrimSpace(state), strings.TrimSpace(userID), expiresAt)
	return err
}

func (s *Service) OAuthStateUser(ctx context.Context, state string) (string, bool, error) {
	var userID string
	err := s.pool.QueryRow(ctx, `
		SELECT multica_user_id::text
		FROM dingtalk_dws_oauth_state
		WHERE state = $1 AND expires_at > now()`, strings.TrimSpace(state)).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return userID, err == nil, err
}

func (s *Service) ConsumeOAuthState(ctx context.Context, state string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM dingtalk_dws_oauth_state WHERE state = $1`, strings.TrimSpace(state))
	return err
}

func (s *Service) SaveCredential(ctx context.Context, userID, dingUserID, unionID string, credential OAuthCredential) error {
	if s == nil {
		return errors.New("DingTalk DWS delivery is not configured")
	}
	corpID := strings.TrimSpace(credential.CorpID)
	if corpID == "" {
		corpID = s.corpID
	}
	if corpID != s.corpID {
		return &DeliveryError{Code: "organization_mismatch", Message: "授权的钉钉组织不是当前允许的乐纯组织。"}
	}
	if credential.AccessToken == "" || strings.TrimSpace(userID) == "" || strings.TrimSpace(dingUserID) == "" {
		return errors.New("DingTalk DWS credential is incomplete")
	}
	accessCiphertext, err := s.box.Seal([]byte(credential.AccessToken))
	if err != nil {
		return fmt.Errorf("encrypt DingTalk access token: %w", err)
	}
	var refreshCiphertext []byte
	if credential.RefreshToken != "" {
		refreshCiphertext, err = s.box.Seal([]byte(credential.RefreshToken))
		if err != nil {
			return fmt.Errorf("encrypt DingTalk refresh token: %w", err)
		}
	}
	if credential.AccessExpiresAt.IsZero() {
		credential.AccessExpiresAt = time.Now().Add(2 * time.Hour)
	}
	if !credential.RefreshExpiresAt.IsZero() && credential.RefreshExpiresAt.Before(time.Now()) {
		return &DeliveryError{Code: "authorization_expired", Message: "钉钉授权已过期，请重新授权。"}
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO dingtalk_dws_credential (
			multica_user_id, corp_id, ding_user_id, union_id, client_id,
			access_token_ciphertext, refresh_token_ciphertext,
			access_expires_at, refresh_expires_at, status, updated_at
		) VALUES ($1, $2, $3, NULLIF($4, ''), $5, $6, $7, $8, $9, 'active', now())
		ON CONFLICT (corp_id, multica_user_id) DO UPDATE SET
			ding_user_id = EXCLUDED.ding_user_id,
			union_id = EXCLUDED.union_id,
			client_id = EXCLUDED.client_id,
			access_token_ciphertext = EXCLUDED.access_token_ciphertext,
			refresh_token_ciphertext = EXCLUDED.refresh_token_ciphertext,
			access_expires_at = EXCLUDED.access_expires_at,
			refresh_expires_at = EXCLUDED.refresh_expires_at,
			status = 'active', last_error_code = NULL, last_error_message = NULL,
			updated_at = now()`, userID, corpID, dingUserID, strings.TrimSpace(unionID),
		firstNonEmpty(credential.ClientID, s.clientID), accessCiphertext, refreshCiphertext,
		credential.AccessExpiresAt, nullableTime(credential.RefreshExpiresAt))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return &DeliveryError{Code: "dws_identity_mismatch", Message: "该钉钉账号已绑定到另一个 Multica 账号，请使用当前登录账号对应的钉钉账号授权。", Cause: err}
		}
		return err
	}
	_, err = s.pool.Exec(ctx, `
		UPDATE dingtalk_personal_message
		SET status = 'pending', available_at = now(), lease_owner = NULL, leased_until = NULL,
			last_error_code = NULL, last_error_message = NULL, updated_at = now()
		WHERE expires_at > now() AND (
			(sender_user_id = $1 AND status IN ('waiting_for_dws_login', 'waiting_for_identity'))
			OR (recipient_user_id = $1 AND status = 'waiting_for_identity')
		)`, userID)
	s.NotifyDingTalkPersonalMessageAvailable(userID)
	return err
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (s *Service) Status(ctx context.Context, userID string) (Status, error) {
	status := Status{Configured: s != nil, State: "authorization_required", Reason: "not_authorized"}
	if s == nil {
		status.State = "unavailable"
		status.Reason = "server_not_configured"
		status.Message = "服务器尚未配置钉钉个人消息加密密钥。"
		return status, nil
	}
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM dingtalk_personal_message
		WHERE sender_user_id = $1 AND expires_at > now()
		  AND status IN ('pending', 'leased', 'waiting_for_dws_login', 'waiting_for_identity')`, userID).Scan(&status.PendingCount); err != nil {
		return Status{}, err
	}
	var credentialStatus string
	var expiresAt time.Time
	var errorCode, errorMessage *string
	err := s.pool.QueryRow(ctx, `
		SELECT status, access_expires_at, last_error_code, last_error_message
		FROM dingtalk_dws_credential
		WHERE multica_user_id = $1 AND corp_id = $2`, userID, s.corpID).
		Scan(&credentialStatus, &expiresAt, &errorCode, &errorMessage)
	if errors.Is(err, pgx.ErrNoRows) {
		return status, nil
	}
	if err != nil {
		return Status{}, err
	}
	status.ExpiresAt = expiresAt.UTC().Format(time.RFC3339)
	if credentialStatus == "active" {
		status.Connected = true
		status.State = "connected"
		status.Reason = ""
		if errorCode != nil && *errorCode != "" {
			status.State = "delivery_error"
			status.Reason = *errorCode
		}
	} else {
		status.State = "authorization_required"
		status.Reason = "authorization_expired"
	}
	if errorMessage != nil {
		status.Message = *errorMessage
	}
	return status, nil
}

func (s *Service) Disconnect(ctx context.Context, userID string) error {
	if s == nil {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE dingtalk_dws_credential
		SET status = 'revoked', access_token_ciphertext = '\\x'::bytea,
			refresh_token_ciphertext = NULL, last_error_code = 'disconnected',
			last_error_message = '钉钉个人消息授权已断开。', updated_at = now()
		WHERE multica_user_id = $1 AND corp_id = $2`, userID, s.corpID)
	return err
}

func (s *Service) run(ctx context.Context) {
	ticker := time.NewTicker(s.workerInterval)
	defer ticker.Stop()
	for {
		s.drain(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.wake:
		}
	}
}

func (s *Service) drain(ctx context.Context) {
	for i := 0; i < 25 && ctx.Err() == nil; i++ {
		message, err := s.claim(ctx)
		if err != nil {
			slog.Warn("dingtalk server personal message claim failed", "error", err)
			return
		}
		if message == nil {
			return
		}
		s.deliver(ctx, *message)
	}
}

type queuedMessage struct {
	ID, SenderUserID, SenderDingUserID, SenderCorpID string
	RecipientDingUserID, Markdown, IdempotencyKey    string
	OpenTaskID                                       string
	Attempts                                         int
}

func (s *Service) claim(ctx context.Context) (*queuedMessage, error) {
	_, _ = s.pool.Exec(ctx, `
		UPDATE dingtalk_personal_message
		SET status = 'expired', lease_owner = NULL, leased_until = NULL,
			last_error_code = 'expired', last_error_message = '消息在服务器投递前已过期', updated_at = now()
		WHERE expires_at <= now() AND status IN ('pending', 'leased', 'waiting_for_dws_login', 'waiting_for_identity')`)
	var message queuedMessage
	var corpID, openTaskID *string
	err := s.pool.QueryRow(ctx, `
		UPDATE dingtalk_personal_message
		SET status = 'leased', attempts = attempts + 1, lease_owner = $1,
			leased_until = now() + interval '2 minutes', updated_at = now()
		WHERE id = (
			SELECT id FROM dingtalk_personal_message
			WHERE expires_at > now() AND available_at <= now()
			  AND (status = 'pending' OR (status = 'leased' AND leased_until < now()))
			ORDER BY created_at ASC FOR UPDATE SKIP LOCKED LIMIT 1
		)
		RETURNING id::text, sender_user_id::text, sender_ding_user_id,
			sender_corp_id, recipient_ding_user_id, markdown, idempotency_key,
			dws_open_task_id, attempts`, workerLeaseOwner).
		Scan(&message.ID, &message.SenderUserID, &message.SenderDingUserID,
			&corpID, &message.RecipientDingUserID, &message.Markdown,
			&message.IdempotencyKey, &openTaskID, &message.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if corpID != nil {
		message.SenderCorpID = strings.TrimSpace(*corpID)
	}
	if openTaskID != nil {
		message.OpenTaskID = strings.TrimSpace(*openTaskID)
	}
	return &message, nil
}

func (s *Service) deliver(ctx context.Context, message queuedMessage) {
	token, err := s.accessToken(ctx, message.SenderUserID, message.SenderCorpID)
	if err != nil {
		s.settle(ctx, message, err)
		return
	}
	if message.OpenTaskID != "" {
		messageID, pending, queryErr := s.querySendStatus(ctx, token, message.OpenTaskID)
		if queryErr != nil {
			s.settle(ctx, message, queryErr)
			return
		}
		if pending {
			s.reschedule(ctx, message, "delivery_pending", "钉钉正在投递消息，将继续查询。", 5*time.Second, false)
			return
		}
		s.markDelivered(ctx, message.ID, message.SenderUserID, message.OpenTaskID, messageID)
		return
	}
	openID, err := s.resolveRecipient(ctx, token, message.RecipientDingUserID)
	if err != nil {
		s.settle(ctx, message, err)
		return
	}
	openTaskID, openMessageID, err := s.send(ctx, token, openID, message.Markdown, message.IdempotencyKey)
	if err != nil {
		s.settle(ctx, message, err)
		return
	}
	if openMessageID != "" {
		s.markDelivered(ctx, message.ID, message.SenderUserID, openTaskID, openMessageID)
		return
	}
	if openTaskID == "" {
		s.settle(ctx, message, &DeliveryError{Code: "missing_delivery_receipt", Message: "钉钉未返回消息投递凭据。", Retryable: true})
		return
	}
	_, _ = s.pool.Exec(ctx, `
		UPDATE dingtalk_personal_message SET status = 'pending', available_at = now() + interval '5 seconds',
			lease_owner = NULL, leased_until = NULL, dws_open_task_id = $2,
			last_error_code = 'delivery_pending', last_error_message = '钉钉正在投递消息，将继续查询。', updated_at = now()
		WHERE id = $1 AND status = 'leased' AND lease_owner = $3`, message.ID, openTaskID, workerLeaseOwner)
}

func (s *Service) settle(ctx context.Context, message queuedMessage, err error) {
	var deliveryErr *DeliveryError
	if !errors.As(err, &deliveryErr) {
		deliveryErr = &DeliveryError{Code: "delivery_failed", Message: "钉钉个人消息发送失败，将自动重试。", Retryable: true, Cause: err}
	}
	if deliveryErr.AuthRequired {
		s.markCredentialReauthorization(ctx, message.SenderUserID, deliveryErr.Code, deliveryErr.Message)
		_, _ = s.pool.Exec(ctx, `
			UPDATE dingtalk_personal_message SET status = 'waiting_for_dws_login',
				lease_owner = NULL, leased_until = NULL, last_error_code = $2,
				last_error_message = $3, updated_at = now()
			WHERE id = $1 AND status = 'leased' AND lease_owner = $4`,
			message.ID, deliveryErr.Code, deliveryErr.Message, workerLeaseOwner)
		return
	}
	if deliveryErr.Identity {
		s.recordCredentialError(ctx, message.SenderUserID, deliveryErr.Code, deliveryErr.Message)
		_, _ = s.pool.Exec(ctx, `
			UPDATE dingtalk_personal_message SET status = 'waiting_for_identity',
				lease_owner = NULL, leased_until = NULL, last_error_code = $2,
				last_error_message = $3, updated_at = now()
			WHERE id = $1 AND status = 'leased' AND lease_owner = $4`,
			message.ID, deliveryErr.Code, deliveryErr.Message, workerLeaseOwner)
		return
	}
	if !deliveryErr.Retryable || message.Attempts >= s.maxAttempts {
		s.recordCredentialError(ctx, message.SenderUserID, deliveryErr.Code, deliveryErr.Message)
		_, _ = s.pool.Exec(ctx, `
			UPDATE dingtalk_personal_message SET status = 'failed', lease_owner = NULL,
				leased_until = NULL, last_error_code = $2, last_error_message = $3, updated_at = now()
			WHERE id = $1 AND status = 'leased' AND lease_owner = $4`,
			message.ID, deliveryErr.Code, deliveryErr.Message, workerLeaseOwner)
		return
	}
	s.reschedule(ctx, message, deliveryErr.Code, deliveryErr.Message, time.Duration(message.Attempts)*time.Minute, false)
}

func (s *Service) reschedule(ctx context.Context, message queuedMessage, code, text string, delay time.Duration, clearTask bool) {
	_, _ = s.pool.Exec(ctx, `
		UPDATE dingtalk_personal_message SET status = 'pending', available_at = now() + $2::interval,
			lease_owner = NULL, leased_until = NULL,
			dws_open_task_id = CASE WHEN $5 THEN NULL ELSE dws_open_task_id END,
			last_error_code = $3, last_error_message = $4, updated_at = now()
		WHERE id = $1 AND status = 'leased' AND lease_owner = $6`,
		message.ID, delay.String(), code, text, clearTask, workerLeaseOwner)
}

func (s *Service) markDelivered(ctx context.Context, id, senderUserID, taskID, messageID string) {
	_, _ = s.pool.Exec(ctx, `
		UPDATE dingtalk_personal_message SET status = 'delivered', lease_owner = NULL,
			leased_until = NULL, dws_open_task_id = NULLIF($2, ''), dws_open_message_id = NULLIF($3, ''),
		last_error_code = NULL, last_error_message = NULL, delivered_at = now(), updated_at = now()
		WHERE id = $1 AND status = 'leased' AND lease_owner = $4`, id, taskID, messageID, workerLeaseOwner)
	_, _ = s.pool.Exec(ctx, `
		UPDATE dingtalk_dws_credential
		SET last_error_code = NULL, last_error_message = NULL, updated_at = now()
		WHERE multica_user_id = $1 AND corp_id = $2 AND status = 'active'`, senderUserID, s.corpID)
}

func (s *Service) accessToken(ctx context.Context, userID, messageCorpID string) (string, error) {
	if messageCorpID != "" && messageCorpID != s.corpID {
		return "", &DeliveryError{Code: "organization_mismatch", Message: "消息所属钉钉组织与服务器配置不一致，请联系管理员检查部署配置。"}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// A transaction-scoped advisory lock prevents two backend replicas from
	// rotating the same refresh token concurrently. The second worker waits,
	// then reads the token written by the first one.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, s.corpID+":"+userID); err != nil {
		return "", err
	}
	var accessCiphertext, refreshCiphertext []byte
	var accessExpiresAt time.Time
	var refreshExpiresAt *time.Time
	var status string
	err = tx.QueryRow(ctx, `
		SELECT access_token_ciphertext, refresh_token_ciphertext, access_expires_at,
			refresh_expires_at, status
		FROM dingtalk_dws_credential
		WHERE multica_user_id = $1 AND corp_id = $2`, userID, s.corpID).
		Scan(&accessCiphertext, &refreshCiphertext, &accessExpiresAt, &refreshExpiresAt, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", &DeliveryError{Code: "not_authorized", Message: "尚未授权服务器发送钉钉个人消息，请完成钉钉授权。", AuthRequired: true}
	}
	if err != nil {
		return "", err
	}
	if status != "active" {
		return "", &DeliveryError{Code: "authorization_expired", Message: "钉钉个人消息授权已失效，请重新授权。", AuthRequired: true}
	}
	accessToken, err := s.box.Open(accessCiphertext)
	if err != nil {
		return "", &DeliveryError{Code: "credential_decryption_failed", Message: "服务器无法读取钉钉授权，请重新授权。", AuthRequired: true, Cause: err}
	}
	if time.Now().Add(5 * time.Minute).Before(accessExpiresAt) {
		if err := tx.Commit(ctx); err != nil {
			return "", err
		}
		return string(accessToken), nil
	}
	if len(refreshCiphertext) == 0 || (refreshExpiresAt != nil && refreshExpiresAt.Before(time.Now())) {
		_, _ = tx.Exec(ctx, `
			UPDATE dingtalk_dws_credential SET status = 'reauthorization_required',
				last_error_code = 'refresh_token_expired', last_error_message = '钉钉授权已过期，请重新授权。', updated_at = now()
			WHERE multica_user_id = $1 AND corp_id = $2`, userID, s.corpID)
		_ = tx.Commit(ctx)
		return "", &DeliveryError{Code: "authorization_expired", Message: "钉钉授权已过期，请重新授权。", AuthRequired: true}
	}
	refreshToken, err := s.box.Open(refreshCiphertext)
	if err != nil {
		_, _ = tx.Exec(ctx, `
			UPDATE dingtalk_dws_credential SET status = 'reauthorization_required',
				last_error_code = 'credential_decryption_failed', last_error_message = '服务器无法读取钉钉授权，请重新授权。', updated_at = now()
			WHERE multica_user_id = $1 AND corp_id = $2`, userID, s.corpID)
		_ = tx.Commit(ctx)
		return "", &DeliveryError{Code: "credential_decryption_failed", Message: "服务器无法读取钉钉授权，请重新授权。", AuthRequired: true, Cause: err}
	}
	credential, err := refreshOAuthToken(ctx, s.httpClient, s.tokenEndpoint, s.clientID, s.clientSecret, string(refreshToken))
	if err != nil {
		var deliveryErr *DeliveryError
		if errors.As(err, &deliveryErr) && deliveryErr.AuthRequired {
			_, _ = tx.Exec(ctx, `
				UPDATE dingtalk_dws_credential SET status = 'reauthorization_required',
					last_error_code = $3, last_error_message = $4, updated_at = now()
				WHERE multica_user_id = $1 AND corp_id = $2`, userID, s.corpID, deliveryErr.Code, deliveryErr.Message)
			_ = tx.Commit(ctx)
		}
		return "", err
	}
	if credential.RefreshToken == "" {
		credential.RefreshToken = string(refreshToken)
	}
	accessCiphertext, err = s.box.Seal([]byte(credential.AccessToken))
	if err != nil {
		return "", fmt.Errorf("encrypt refreshed DingTalk access token: %w", err)
	}
	refreshCiphertext, err = s.box.Seal([]byte(credential.RefreshToken))
	if err != nil {
		return "", fmt.Errorf("encrypt refreshed DingTalk refresh token: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE dingtalk_dws_credential
		SET access_token_ciphertext = $3, refresh_token_ciphertext = $4,
			access_expires_at = $5, refresh_expires_at = $6, client_id = $7,
			status = 'active', last_error_code = NULL, last_error_message = NULL,
			last_refreshed_at = now(), updated_at = now()
		WHERE multica_user_id = $1 AND corp_id = $2`, userID, s.corpID,
		accessCiphertext, refreshCiphertext, credential.AccessExpiresAt,
		nullableTime(credential.RefreshExpiresAt), s.clientID); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return credential.AccessToken, nil
}

func (s *Service) markCredentialReauthorization(ctx context.Context, userID, code, message string) {
	_, _ = s.pool.Exec(ctx, `
		UPDATE dingtalk_dws_credential SET status = 'reauthorization_required',
			last_error_code = $3, last_error_message = $4, updated_at = now()
		WHERE multica_user_id = $1 AND corp_id = $2`, userID, s.corpID, code, message)
}

func (s *Service) recordCredentialError(ctx context.Context, userID, code, message string) {
	_, _ = s.pool.Exec(ctx, `
		UPDATE dingtalk_dws_credential
		SET last_error_code = $3, last_error_message = $4, updated_at = now()
		WHERE multica_user_id = $1 AND corp_id = $2 AND status = 'active'`, userID, s.corpID, code, message)
}

type DeliveryError struct {
	Code         string
	Message      string
	Retryable    bool
	AuthRequired bool
	Identity     bool
	Cause        error
}

func (e *DeliveryError) Error() string {
	if e == nil {
		return "DingTalk delivery failed"
	}
	if e.Cause != nil {
		return e.Message + ": " + e.Cause.Error()
	}
	return e.Message
}

func (e *DeliveryError) Unwrap() error { return e.Cause }

func (s *Service) logFailure(message queuedMessage, err error) {
	slog.Warn("dingtalk server personal message delivery failed", "message_id", message.ID, "error", err)
}

func encodeContent(title, text string) string {
	data, _ := json.Marshal(map[string]string{"title": title, "text": text})
	return string(data)
}
