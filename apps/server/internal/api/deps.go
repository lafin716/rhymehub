package api

import (
	"context"
	"errors"
)

// ErrNotConfigured is returned by the default collaborators when no implementation was injected.
var ErrNotConfigured = errors.New("not configured")

// Releaser finishes an approved task. The task is already in MERGING when this is called (the approve
// handler performs APPROVED -> MERGING). The implementation merges the branch and, when release is true,
// runs the release (MERGING -> RELEASING -> COMPLETED). When release is false it stops at COMPLETED
// (MERGING -> COMPLETED). On error the server moves a task that is still MERGING to FAILED.
// The release manager is owned by another component; the server only depends on this interface. Calls are
// made asynchronously after approval, so implementations must not assume the HTTP request is still open.
type Releaser interface {
	ReleaseTask(ctx context.Context, taskID string, release bool) error
}

// Canceller stops the worker process of a running task. It is called after a RUNNING, PREPARING or
// VALIDATING task was moved to CANCELLED. Nil means no process control is available.
type Canceller interface {
	CancelTask(ctx context.Context, taskID string) error
}

// Supervisor forwards self-update operations to the supervisor (127.0.0.1:7781, config.SupervisorAddr).
type Supervisor interface {
	// Update asks the supervisor to install the given version.
	Update(ctx context.Context, version string) error
	// Rollback asks the supervisor to roll back the update identified by updateID.
	Rollback(ctx context.Context, updateID string) error
}

type unconfiguredReleaser struct{}

func (unconfiguredReleaser) ReleaseTask(context.Context, string, bool) error { return ErrNotConfigured }
