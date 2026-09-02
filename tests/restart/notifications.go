package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	agentv1connect "github.com/yangtao121/workos/gen/go/workos/agent/v1/agentv1connect"
	artifactv1 "github.com/yangtao121/workos/gen/go/workos/artifact/v1"
	artifactv1connect "github.com/yangtao121/workos/gen/go/workos/artifact/v1/artifactv1connect"
	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	notificationv1 "github.com/yangtao121/workos/gen/go/workos/notification/v1"
	notificationv1connect "github.com/yangtao121/workos/gen/go/workos/notification/v1/notificationv1connect"
	projectv1 "github.com/yangtao121/workos/gen/go/workos/project/v1"
	projectv1connect "github.com/yangtao121/workos/gen/go/workos/project/v1/projectv1connect"
)

// notificationsSeed runs one fake-harness task to terminal and waits for the
// two owner notifications its source transactions must project (task
// terminal + review artifact). It reads the terminal one through the durable
// idempotent read command and prints
// "TASK_ID READ_ID UNREAD_ID UNREAD_COUNT" for the restart verify step.
func notificationsSeed(ctx context.Context, client *http.Client, baseURL string) error {
	projects := projectv1connect.NewProjectServiceClient(client, baseURL)
	bindings := projectv1connect.NewProjectHarnessBindingServiceClient(client, baseURL)
	tasks := agentv1connect.NewAgentTaskServiceClient(client, baseURL)
	artifacts := artifactv1connect.NewArtifactServiceClient(client, baseURL)
	notifications := notificationv1connect.NewNotificationServiceClient(client, baseURL)

	stamp := time.Now().UnixNano()
	created, err := projects.CreateProject(ctx, connect.NewRequest(&projectv1.CreateProjectRequest{
		IdempotencyKey: fmt.Sprintf("restart-notifications-%d", stamp), Name: "Restart Notifications",
	}))
	if err != nil {
		return fmt.Errorf("create notifications project: %w", err)
	}
	project := created.Msg.GetProject()
	if _, err := bindings.SetProjectHarnessBinding(ctx, connect.NewRequest(&projectv1.SetProjectHarnessBindingRequest{
		ProjectId: project.GetId(), ExpectedRevision: project.GetRevision(),
		Selection: &projectv1.SetProjectHarnessBindingRequest_ProviderId{ProviderId: "fake"},
	})); err != nil {
		return fmt.Errorf("bind fake provider: %w", err)
	}
	submitted, err := tasks.SubmitTask(ctx, connect.NewRequest(&agentv1.SubmitTaskRequest{
		IdempotencyKey: fmt.Sprintf("restart-notifications-task-%d", stamp),
		Input: &agentv1.AgentTaskInput{
			TargetScope:         &agentv1.TargetScope{Scope: &agentv1.TargetScope_ProjectId{ProjectId: project.GetId()}},
			Role:                "general",
			Goal:                "restart persistence notification review",
			OutputArtifactTypes: []string{"document.markdown.v1"},
		},
	}))
	if err != nil {
		return fmt.Errorf("submit notifications task: %w", err)
	}
	taskID := submitted.Msg.GetTask().GetId()

	var artifactID string
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) && artifactID == "" {
		listed, err := artifacts.ListArtifacts(ctx, connect.NewRequest(&artifactv1.ListArtifactsRequest{
			ProjectId: project.GetId(), Page: &commonv1.PageRequest{PageSize: 10},
		}))
		if err == nil && len(listed.Msg.GetArtifacts()) > 0 {
			artifactID = listed.Msg.GetArtifacts()[0].GetId()
		} else {
			time.Sleep(500 * time.Millisecond)
		}
	}
	if artifactID == "" {
		return errors.New("review artifact never materialized")
	}

	var readID, unreadID string
	for time.Now().Before(deadline) && (readID == "" || unreadID == "") {
		page, err := notifications.ListNotifications(ctx, connect.NewRequest(&notificationv1.ListNotificationsRequest{
			PageSize: 100,
		}))
		if err == nil {
			for _, fact := range page.Msg.GetNotifications() {
				if fact.GetReadAt() != nil {
					continue
				}
				switch fact.GetKind() {
				case notificationv1.NotificationKind_NOTIFICATION_KIND_AGENT_TASK_TERMINAL:
					if readID == "" {
						readID = fact.GetId()
					}
				case notificationv1.NotificationKind_NOTIFICATION_KIND_ARTIFACT_REVIEW_CREATED:
					if unreadID == "" {
						unreadID = fact.GetId()
					}
				}
			}
		}
		if readID == "" || unreadID == "" {
			time.Sleep(500 * time.Millisecond)
		}
	}
	if readID == "" || unreadID == "" {
		return errors.New("projected notifications never appeared")
	}

	readKey := fmt.Sprintf("restart-notifications-read-%d", stamp)
	read, err := notifications.MarkNotificationRead(ctx, connect.NewRequest(&notificationv1.MarkNotificationReadRequest{
		NotificationId: readID,
		IdempotencyKey: readKey,
	}))
	if err != nil {
		return fmt.Errorf("mark read: %w", err)
	}
	fmt.Printf("%s %s %s %s %d\n", taskID, readID, unreadID, readKey, read.Msg.GetUnreadCount())
	return nil
}

// notificationsVerify re-checks the notification facts after a full Core +
// harness + runtime + gateway restart: the read fact is still read, the
// unread fact is still unread with the same unread count, and replaying the
// consumed read key replays the exact first response instead of drifting.
func notificationsVerify(ctx context.Context, client *http.Client, baseURL, readID, unreadID, readKey string, unreadAtSeed int64) error {
	notifications := notificationv1connect.NewNotificationServiceClient(client, baseURL)
	defer func() {
		fmt.Printf("notification persistence verified for unread fact %s\n", unreadID)
	}()

	summary, err := notifications.GetNotificationSummary(ctx, connect.NewRequest(&notificationv1.GetNotificationSummaryRequest{}))
	if err != nil {
		return fmt.Errorf("summary: %w", err)
	}
	if summary.Msg.GetUnreadCount() < unreadAtSeed {
		return fmt.Errorf("unread count drifted down across restart: %d < %d",
			summary.Msg.GetUnreadCount(), unreadAtSeed)
	}

	page, err := notifications.ListNotifications(ctx, connect.NewRequest(&notificationv1.ListNotificationsRequest{
		PageSize: 100,
	}))
	if err != nil {
		return fmt.Errorf("list: %w", err)
	}
	var stillRead, stillUnread bool
	for _, fact := range page.Msg.GetNotifications() {
		if fact.GetId() == readID {
			if fact.GetReadAt() == nil {
				return errors.New("the read fact lost its read projection across restart")
			}
			stillRead = true
		}
		if fact.GetId() == unreadID {
			if fact.GetReadAt() != nil {
				return errors.New("the unread fact was mysteriously read across restart")
			}
			stillUnread = true
		}
	}
	if !stillRead || !stillUnread {
		return errors.New("seeded notification facts are missing after restart")
	}

	// The consumed read key replays the exact first response across restart
	// instead of re-applying or conflicting.
	replayed, err := notifications.MarkNotificationRead(ctx, connect.NewRequest(&notificationv1.MarkNotificationReadRequest{
		NotificationId: readID,
		IdempotencyKey: readKey,
	}))
	if err != nil {
		return fmt.Errorf("read key replay: %w", err)
	}
	if replayed.Msg.GetNotification().GetId() != readID || replayed.Msg.GetUnreadCount() < unreadAtSeed {
		return errors.New("read key replay drifted from the first response")
	}
	return nil
}
