package notify

import (
	"context"
	"errors"
	"math"
	"time"
)

// RetryableError lets providers classify transient failures (timeouts,
// throttling and token refreshes) without coupling this module to an HTTP SDK.
type RetryableError struct{ Err error }

func (e RetryableError) Error() string { return e.Err.Error() }
func (e RetryableError) Unwrap() error { return e.Err }

type RetryPolicy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

func (p RetryPolicy) normalized() RetryPolicy {
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = 5
	}
	if p.BaseDelay <= 0 {
		p.BaseDelay = time.Second
	}
	if p.MaxDelay <= 0 {
		p.MaxDelay = 5 * time.Minute
	}
	return p
}

type Worker struct {
	Store    Store
	Provider Provider
	Policy   RetryPolicy
	Now      func() time.Time
	Audit    AuditSink
}

// RunOnce claims due rows and handles each row independently. A provider
// failure never aborts the batch; the source comment remains successful.
func (w Worker) RunOnce(ctx context.Context, limit int) (int, error) {
	if w.Store == nil || w.Provider == nil {
		return 0, errors.New("worker store and provider are required")
	}
	now := time.Now()
	if w.Now != nil {
		now = w.Now()
	}
	items, err := w.Store.ClaimDue(ctx, now, limit)
	if err != nil {
		return 0, err
	}
	policy := w.Policy.normalized()
	for _, item := range items {
		started := time.Now()
		sendErr := w.Provider.Send(ctx, item.Message)
		if sendErr == nil {
			if err := w.Store.MarkDelivered(ctx, item.ID, now); err != nil {
				return len(items), err
			}
			w.recordAudit(ctx, item, StatusDelivered, "", started)
			continue
		}
		if isRetryable(sendErr) && item.Attempts < policy.MaxAttempts {
			delay := backoff(policy, item.Attempts)
			if err := w.Store.MarkRetry(ctx, item.ID, now.Add(delay), item.Attempts, sendErr.Error()); err != nil {
				return len(items), err
			}
			w.recordAudit(ctx, item, StatusPending, sendErr.Error(), started)
			continue
		}
		if err := w.Store.MarkFailed(ctx, item.ID, now, item.Attempts, sendErr.Error()); err != nil {
			return len(items), err
		}
		w.recordAudit(ctx, item, StatusFailed, sendErr.Error(), started)
	}
	return len(items), nil
}

func (w Worker) recordAudit(ctx context.Context, item OutboxItem, status, reason string, started time.Time) {
	if w.Audit == nil {
		return
	}
	_ = w.Audit.Record(ctx, DeliveryAudit{OutboxID: item.ID, EventID: item.Message.EventID,
		WorkspaceID: item.Message.WorkspaceID, TargetID: item.Message.TargetID,
		TargetKind: item.Message.TargetKind, ChannelType: item.Message.ChannelType,
		Status: status, Attempts: item.Attempts, Error: reason, Duration: time.Since(started), At: time.Now()})
}

// Run keeps the worker in the caller's process. It is intentionally
// synchronous so a host can supervise, cancel, and expose health state.
func (w Worker) Run(ctx context.Context, interval time.Duration, limit int) error {
	if interval <= 0 {
		interval = time.Second
	}
	if _, err := w.RunOnce(ctx, limit); err != nil {
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := w.RunOnce(ctx, limit); err != nil {
				return err
			}
		}
	}
}

func isRetryable(err error) bool {
	var retryable RetryableError
	return errors.As(err, &retryable)
}

func backoff(policy RetryPolicy, attempts int) time.Duration {
	factor := math.Pow(2, float64(max(0, attempts-1)))
	delay := time.Duration(float64(policy.BaseDelay) * factor)
	if delay > policy.MaxDelay {
		return policy.MaxDelay
	}
	return delay
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
