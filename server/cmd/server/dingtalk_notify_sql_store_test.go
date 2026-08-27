package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	notify "github.com/lechun-dev/multica/extensions/dingtalk-notify"
)

func TestDingTalkNotifySQLStoreClaimsDueMessageWithSimpleProtocol(t *testing.T) {
	ensureDingTalkProfileSyncSchema(t)

	connConfig := *testPool.Config().ConnConfig
	connConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	sqlDB := stdlib.OpenDB(connConfig)
	t.Cleanup(func() { _ = sqlDB.Close() })

	ctx := context.Background()
	now := time.Now().UTC()
	key := fmt.Sprintf("%s-%d", t.Name(), now.UnixNano())
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(),
			`DELETE FROM dingtalk_notify_outbox WHERE idempotency_key = $1`, key)
	})

	store := &notify.SQLStore{DB: sqlDB, Lease: 2 * time.Minute}
	created, err := store.Enqueue(ctx, notify.OutboxItem{
		ID:             key,
		IdempotencyKey: key,
		Message: notify.Message{
			EventID:     key,
			WorkspaceID: "workspace-1",
			TargetID:    "member-1",
			TargetKind:  "member",
			DingUserID:  "ding-user-1",
			ChannelType: "p2p",
			Text:        "test notification",
		},
		Status:        notify.StatusPending,
		NextAttemptAt: now.Add(-time.Second),
		CreatedAt:     now,
		UpdatedAt:     now,
	})
	if err != nil {
		t.Fatalf("enqueue notification: %v", err)
	}
	if !created {
		t.Fatal("notification was not created")
	}

	claimed, err := store.ClaimDue(ctx, now, 1)
	if err != nil {
		t.Fatalf("claim due notification: %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed %d notifications, want 1", len(claimed))
	}
	if claimed[0].ID != key || claimed[0].Status != notify.StatusProcessing || claimed[0].Attempts != 1 {
		t.Fatalf("claimed notification = %+v, want id=%q status=processing attempts=1", claimed[0], key)
	}

	again, err := store.ClaimDue(ctx, now, 1)
	if err != nil {
		t.Fatalf("claim leased notification again: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("leased notification was claimed twice: %+v", again)
	}
}
