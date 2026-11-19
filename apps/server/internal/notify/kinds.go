package notify

import (
	"context"
	"fmt"
	"strings"

	"github.com/lafin716/rhymehub/internal/domain"
)

// Notification kinds. The strings are part of the API (SSE notification events and the notifications list).
const (
	KindReviewReady      = "review_ready"
	KindWaitingInput     = "waiting_input"
	KindWaitingApproval  = "waiting_approval"
	KindFailedTests      = "failed_tests"
	KindFailedBuild      = "failed_build"
	KindReleaseSucceeded = "release_succeeded"
	KindReleaseFailed    = "release_failed"
	KindUpdateSucceeded  = "update_succeeded"
	KindUpdateFailed     = "update_failed"
)

// Draft is a notification ready for Enqueue. Its fields map one-to-one to Enqueue's arguments.
type Draft struct {
	Kind      string
	EventKey  string
	Title     string
	Body      string
	URL       string
	ProjectID string
	TaskID    string
}

// EnqueueDraft stores d through Enqueue.
func (s *Service) EnqueueDraft(ctx context.Context, d Draft) (domain.Notification, bool, error) {
	return s.Enqueue(ctx, d.Kind, d.EventKey, d.Title, d.Body, d.URL, d.ProjectID, d.TaskID)
}

// ForTaskTransition maps a task state change to a notification. task must already reflect the new state (the
// version is part of the dedupe key, so the same transition replayed with the same version is not sent twice).
// The second result is false when the state does not warrant a notification.
//
// Failed tasks notify only when FailureReason names the test or build step. Other failures stay in the task log.
func ForTaskTransition(task domain.Task, to domain.TaskState) (Draft, bool) {
	d := Draft{
		EventKey:  fmt.Sprintf("task:%s:%s:%d", task.ID, to, task.Version),
		Body:      task.Title,
		URL:       "/tasks/" + task.ID,
		ProjectID: task.ProjectID,
		TaskID:    task.ID,
	}
	switch to {
	case domain.StateReviewReady:
		d.Kind, d.Title = KindReviewReady, "Ready for review"
	case domain.StateWaitingInput:
		d.Kind, d.Title = KindWaitingInput, "Agent needs your answer"
	case domain.StateWaitingApproval:
		d.Kind, d.Title = KindWaitingApproval, "Agent needs permission"
	case domain.StateFailed:
		reason := strings.ToLower(task.FailureReason)
		switch {
		case strings.Contains(reason, "test"):
			d.Kind, d.Title = KindFailedTests, "Tests failed"
		case strings.Contains(reason, "build"):
			d.Kind, d.Title = KindFailedBuild, "Build failed"
		default:
			return Draft{}, false
		}
	default:
		return Draft{}, false
	}
	return d, true
}

// ForRelease builds the notification for a finished release. eventKey should be stable per release, for example
// "release:<id>:<status>".
func ForRelease(rel domain.Release, projectID string) Draft {
	d := Draft{
		EventKey:  fmt.Sprintf("release:%s:%s", rel.ID, rel.Status),
		Body:      "Version " + rel.Version,
		URL:       "/releases/" + rel.ID,
		ProjectID: projectID,
	}
	if rel.Status == domain.ReleasePublished {
		d.Kind, d.Title = KindReleaseSucceeded, "Release published"
	} else {
		d.Kind, d.Title = KindReleaseFailed, "Release failed"
	}
	return d
}

// ForUpdate builds the notification for an update attempt. version is the target version and status is
// "succeeded" or "failed". The key is stable per (version, status).
func ForUpdate(version string, succeeded bool) Draft {
	d := Draft{
		EventKey: fmt.Sprintf("system:update:%s:%t", version, succeeded),
		Body:     "Version " + version,
		URL:      "/settings/updates",
	}
	if succeeded {
		d.Kind, d.Title = KindUpdateSucceeded, "Update installed"
	} else {
		d.Kind, d.Title = KindUpdateFailed, "Update failed"
	}
	return d
}
