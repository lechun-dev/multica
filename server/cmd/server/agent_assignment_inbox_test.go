package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/projectauth"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func agentAssignmentViewAuthorizer(queries *db.Queries) issueViewAuthorizer {
	h := &handler.Handler{
		Queries: queries, DB: testPool,
		ProjectAuth: projectauth.NewWithRollout(nil, projectauth.RolloutRestricted),
	}
	return h.CanMemberViewIssue
}

func TestAgentAssignmentInbox_NotifiesAndSubscribesHumanOwner(t *testing.T) {
	queries := db.New(testPool)
	fixture := testutil.New(testPool, testWorkspaceID, testUserID)
	owner := fixture.User(t, "Agent owner", "agent-assignment-owner@multica.test")
	fixture.Member(t, testWorkspaceID, owner, "member")
	source := fixture.Agent(t, "Delegating agent", "")
	target := fixture.Agent(t, "Assigned agent", "", testutil.Cols{"owner_id": owner})
	id := fixture.Issue(t, "Agent assignment inbox", testutil.Cols{
		"creator_type": "agent", "creator_id": source, "assignee_type": "agent", "assignee_id": target,
	})
	t.Cleanup(func() { cleanupInboxForIssue(t, id) })

	bus := events.New()
	registerSubscriberListeners(bus, testPool)
	registerNotificationListeners(bus, queries, testPool, agentAssignmentViewAuthorizer(queries))
	ownerAssignmentEvents := 0
	bus.Subscribe(protocol.EventInboxNew, func(e events.Event) {
		item := e.Payload.(map[string]any)["item"].(map[string]any)
		if item["recipient_type"] == "member" && item["recipient_id"] == owner && item["type"] == "issue_assigned" {
			ownerAssignmentEvents++
		}
	})
	agent := "agent"
	issue := handler.IssueResponse{
		ID: id, WorkspaceID: testWorkspaceID, Title: "Agent assignment inbox", Status: "todo",
		CreatorType: "agent", CreatorID: source, AssigneeType: &agent, AssigneeID: &target,
	}
	bus.Publish(events.Event{
		Type: protocol.EventIssueCreated, WorkspaceID: testWorkspaceID, ActorType: "agent", ActorID: source,
		Payload: map[string]any{"issue": issue},
	})

	items := inboxItemsForRecipient(t, queries, owner)
	if len(items) != 1 || items[0].Type != "issue_assigned" || util.UUIDToString(items[0].IssueID) != id {
		t.Fatalf("agent owner should get one personal assignment notification, got %+v", items)
	}
	if ownerAssignmentEvents != 1 {
		t.Fatalf("agent owner should get one realtime assignment event, got %d", ownerAssignmentEvents)
	}
	var details map[string]string
	if err := json.Unmarshal(items[0].Details, &details); err != nil {
		t.Fatal(err)
	}
	if details["new_assignee_type"] != "agent" || details["new_assignee_id"] != target {
		t.Fatalf("assignment should identify the agent, not the human recipient: %v", details)
	}
	if !isSubscribed(t, queries, id, "member", owner) || !isSubscribed(t, queries, id, "agent", target) {
		t.Fatal("the agent and its human owner should both be subscribed")
	}
	if items := inboxItemsForRecipient(t, queries, testUserID); len(items) != 0 || isSubscribed(t, queries, id, "member", testUserID) {
		t.Fatal("neither the workspace owner nor the creating agent's owner should be added as a recipient solely for that role")
	}

	bus.Publish(events.Event{
		Type: protocol.EventCommentCreated, WorkspaceID: testWorkspaceID, ActorType: "agent", ActorID: target,
		Payload: map[string]any{
			"comment":     handler.CommentResponse{IssueID: id, AuthorType: "agent", AuthorID: target, Content: "Execution result"},
			"issue_title": issue.Title, "issue_status": issue.Status,
		},
	})
	bus.Publish(events.Event{
		Type: protocol.EventIssueUpdated, WorkspaceID: testWorkspaceID, ActorType: "agent", ActorID: target,
		Payload: map[string]any{
			"issue": issue, "status_changed": true, "prev_status": "in_progress",
		},
	})
	counts := make(map[string]int)
	for _, item := range inboxItemsForRecipient(t, queries, owner) {
		counts[item.Type]++
	}
	if counts["issue_assigned"] != 1 || counts["new_comment"] != 1 || counts["status_changed"] != 1 {
		t.Fatalf("assignment should lead to ordinary progress notifications, got %v", counts)
	}
}

func TestAgentAssignmentInbox_Reassignment(t *testing.T) {
	for _, tc := range []struct {
		name             string
		previousWasOwner bool
		mentioned        bool
		muted            bool
		optOut           string
		wantNotice       string
	}{
		{name: "upgrade existing delegated subscription", wantNotice: "issue_assigned"},
		{name: "previous assignee and mentioned owner", previousWasOwner: true, mentioned: true, wantNotice: "issue_assigned"},
		{name: "explicit mention survives muted assignment", mentioned: true, muted: true, wantNotice: "mentioned"},
		{name: "silent assignment still subscribes", muted: true},
		{name: "task opt-out remains", optOut: "issue", wantNotice: "issue_assigned"},
		{name: "parent subtree opt-out remains", optOut: "subtree", wantNotice: "issue_assigned"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			queries := db.New(testPool)
			fixture := testutil.New(testPool, testWorkspaceID, testUserID)
			owner := fixture.User(t, "Transfer owner", "agent-assignment-transfer@multica.test")
			fixture.Member(t, testWorkspaceID, owner, "member")
			target := fixture.Agent(t, "Assigned agent", "", testutil.Cols{"owner_id": owner})
			cols := testutil.Cols{"assignee_type": "agent", "assignee_id": target}
			if tc.optOut == "subtree" {
				parent := fixture.Issue(t, "Opted-out parent")
				cols["parent_issue_id"] = parent
				if _, err := queries.UnsubscribeFromIssueSubtree(ctx, db.UnsubscribeFromIssueSubtreeParams{
					ID: util.MustParseUUID(parent), UserType: "member", UserID: util.MustParseUUID(owner),
				}); err != nil {
					t.Fatal(err)
				}
			}
			id := fixture.Issue(t, "Transferred task", cols)
			t.Cleanup(func() { cleanupInboxForIssue(t, id) })
			if tc.optOut != "subtree" {
				addTestSubscriber(t, id, "member", owner, "delegated")
			}
			if tc.optOut == "issue" {
				if err := queries.RemoveIssueSubscriber(ctx, db.RemoveIssueSubscriberParams{
					IssueID: util.MustParseUUID(id), UserType: "member", UserID: util.MustParseUUID(owner),
				}); err != nil {
					t.Fatal(err)
				}
			}
			if tc.muted {
				if _, err := queries.UpsertNotificationPreference(ctx, db.UpsertNotificationPreferenceParams{
					WorkspaceID: util.MustParseUUID(testWorkspaceID),
					UserID:      util.MustParseUUID(owner),
					Preferences: []byte(`{"assignments":"muted"}`),
				}); err != nil {
					t.Fatal(err)
				}
			}
			bus := events.New()
			registerSubscriberListeners(bus, testPool)
			registerNotificationListeners(bus, queries, testPool, agentAssignmentViewAuthorizer(queries))
			agent, member := "agent", "member"
			issue := handler.IssueResponse{
				ID: id, WorkspaceID: testWorkspaceID, Title: "Transferred task", Status: "todo",
				AssigneeType: &agent, AssigneeID: &target,
			}
			change := events.Event{
				Type: protocol.EventIssueUpdated, WorkspaceID: testWorkspaceID, ActorType: "member", ActorID: testUserID,
				Payload: map[string]any{"issue": issue, "assignee_changed": true},
			}
			payload := change.Payload.(map[string]any)
			if tc.previousWasOwner {
				payload["prev_assignee_type"], payload["prev_assignee_id"] = &member, &owner
			}
			if tc.mentioned {
				description := "FYI [@Owner](mention://member/" + owner + ")"
				issue.Description = &description
				payload["issue"], payload["description_changed"] = issue, true
			}
			bus.Publish(change)
			bus.Publish(events.Event{
				Type: protocol.EventIssueUpdated, WorkspaceID: testWorkspaceID, ActorType: "member", ActorID: testUserID,
				Payload: map[string]any{"issue": issue, "assignee_changed": false, "prev_assignee_type": &agent, "prev_assignee_id": &target},
			})

			items := inboxItemsForRecipient(t, queries, owner)
			if tc.wantNotice == "" {
				if len(items) != 0 {
					t.Fatalf("muted assignment should not notify, got %+v", items)
				}
			} else if len(items) != 1 || items[0].Type != tc.wantNotice || util.UUIDToString(items[0].IssueID) != id {
				t.Fatalf("expected exactly one %s for the transfer and none for the no-op, got %+v", tc.wantNotice, items)
			}
			if got := isSubscribed(t, queries, id, "member", owner); got != (tc.optOut == "") {
				t.Fatalf("owner subscription = %v, opt-out scope = %q", got, tc.optOut)
			}
			if !isSubscribed(t, queries, id, "agent", target) {
				t.Fatal("reassignment must still subscribe the Agent")
			}
			if tc.optOut == "" && !tc.mentioned {
				var reason string
				fixture.QueryRow(t, "SELECT reason FROM issue_subscriber WHERE issue_id = $1 AND user_type = 'member' AND user_id = $2", id, owner).Scan(&reason)
				if reason != "assignee" {
					t.Fatalf("existing delegated subscription should be upgraded, got %s", reason)
				}
			}
		})
	}
}

func TestAgentAssignmentInbox_RespectsWorkspaceAndView(t *testing.T) {
	for _, scenario := range []string{"former member", "different workspace", "ownerless agent", "view denied", "authorization error"} {
		t.Run(scenario, func(t *testing.T) {
			queries := db.New(testPool)
			fixture := testutil.New(testPool, testWorkspaceID, testUserID)
			owner := fixture.User(t, "Other owner", "agent-assignment-other@multica.test")
			if scenario != "former member" {
				fixture.Member(t, testWorkspaceID, owner, "member")
			}
			cols := testutil.Cols{"owner_id": owner}
			switch scenario {
			case "different workspace":
				cols["workspace_id"] = fixture.Workspace(t, "Other workspace", "agent-assignment-other")
			case "ownerless agent":
				cols["owner_id"] = nil
			}
			target := fixture.Agent(t, "Agent outside notification scope", "", cols)
			assignment := testutil.Cols{"assignee_type": "agent", "assignee_id": target}
			if scenario == "view denied" {
				// 2026-11-05 coder(lq): A stale event must not override current View after the Agent was removed.
				assignment = nil
			}
			id := fixture.Issue(t, "Owner cannot be notified", assignment)
			t.Cleanup(func() { cleanupInboxForIssue(t, id) })
			authorizer := agentAssignmentViewAuthorizer(queries)
			if scenario == "authorization error" {
				authorizer = func(context.Context, string, string, string) (bool, error) {
					return false, errors.New("authorization unavailable")
				}
			}
			bus := events.New()
			registerNotificationListeners(bus, queries, testPool, authorizer)
			ownerEvent := false
			bus.Subscribe(protocol.EventInboxNew, func(e events.Event) {
				item := e.Payload.(map[string]any)["item"].(map[string]any)
				ownerEvent = ownerEvent || item["recipient_id"] == owner
			})
			agent := "agent"
			bus.Publish(events.Event{
				Type: protocol.EventIssueCreated, WorkspaceID: testWorkspaceID, ActorType: "member", ActorID: testUserID,
				Payload: map[string]any{"issue": handler.IssueResponse{
					ID: id, WorkspaceID: testWorkspaceID, Title: "Owner cannot be notified", Status: "todo",
					AssigneeType: &agent, AssigneeID: &target,
				}},
			})
			if items := inboxItemsForRecipient(t, queries, owner); len(items) != 0 || ownerEvent || isSubscribed(t, queries, id, "member", owner) {
				t.Fatalf("should not write or broadcast personal inbox or subscribe out-of-scope owner; items = %+v, event = %v", items, ownerEvent)
			}
		})
	}
}
