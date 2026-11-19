package domain

import (
	"fmt"
	"slices"
)

// TaskState is the persisted state of a task (tasks.state).
type TaskState string

// Task states. The string values are stored in SQLite and used in the API; do not rename.
const (
	StateCreated         TaskState = "CREATED"
	StateQueued          TaskState = "QUEUED"
	StatePreparing       TaskState = "PREPARING"
	StateRunning         TaskState = "RUNNING"
	StateWaitingInput    TaskState = "WAITING_INPUT"
	StateWaitingApproval TaskState = "WAITING_APPROVAL"
	StateValidating      TaskState = "VALIDATING"
	StateReviewReady     TaskState = "REVIEW_READY"
	StateApproved        TaskState = "APPROVED"
	StateMerging         TaskState = "MERGING"
	StateReleasing       TaskState = "RELEASING"
	StateCompleted       TaskState = "COMPLETED"
	StateFailed          TaskState = "FAILED"
	StateCancelled       TaskState = "CANCELLED"
	StateRetrying        TaskState = "RETRYING"
	StateRolledBack      TaskState = "ROLLED_BACK"
)

// allStates lists every state in declaration order.
var allStates = []TaskState{
	StateCreated, StateQueued, StatePreparing, StateRunning, StateWaitingInput, StateWaitingApproval,
	StateValidating, StateReviewReady, StateApproved, StateMerging, StateReleasing, StateCompleted,
	StateFailed, StateCancelled, StateRetrying, StateRolledBack,
}

// transitions is the state machine from docs/ARCHITECTURE.md §4. Keep it in sync with that table.
var transitions = map[TaskState][]TaskState{
	StateCreated:         {StateQueued, StateCancelled},
	StateQueued:          {StatePreparing, StateCancelled},
	StatePreparing:       {StateRunning, StateFailed, StateCancelled, StateRetrying},
	StateRunning:         {StateWaitingInput, StateWaitingApproval, StateValidating, StateFailed, StateCancelled, StateRetrying},
	StateWaitingInput:    {StateQueued, StateCancelled},
	StateWaitingApproval: {StateQueued, StateCancelled},
	StateValidating:      {StateReviewReady, StateFailed, StateCancelled, StateRetrying},
	StateReviewReady:     {StateApproved, StateQueued, StateCancelled},
	StateApproved:        {StateMerging},
	StateMerging:         {StateReleasing, StateCompleted, StateFailed},
	StateReleasing:       {StateCompleted, StateFailed},
	StateFailed:          {StateQueued, StateRetrying, StateCancelled},
	StateRetrying:        {StateQueued, StateFailed},
	StateCompleted:       {StateRolledBack},
	StateCancelled:       nil,
	StateRolledBack:      nil,
}

// AllStates returns every known task state.
func AllStates() []TaskState {
	return slices.Clone(allStates)
}

// Valid reports whether s is a known task state.
func (s TaskState) Valid() bool {
	return slices.Contains(allStates, s)
}

// CanTransition reports whether the state machine allows from -> to.
func CanTransition(from, to TaskState) bool {
	return slices.Contains(transitions[from], to)
}

// ValidTransitions returns the states reachable in one step from s.
func ValidTransitions(from TaskState) []TaskState {
	return slices.Clone(transitions[from])
}

// CheckTransition returns nil if from -> to is allowed, otherwise an error wrapping ErrIllegalTransition.
func CheckTransition(from, to TaskState) error {
	if CanTransition(from, to) {
		return nil
	}
	return fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, from, to)
}

// IsTerminal reports whether no further transitions are possible from s.
// COMPLETED is not terminal because it can move to ROLLED_BACK.
func IsTerminal(s TaskState) bool {
	return len(transitions[s]) == 0
}

// IsRunning reports whether the task occupies a scheduler execution slot
// (global, per-project and per-agent limits count PREPARING|RUNNING|VALIDATING).
func IsRunning(s TaskState) bool {
	switch s {
	case StatePreparing, StateRunning, StateValidating:
		return true
	}
	return false
}

// IsActive reports whether the task is still in flight or waiting on someone:
// everything except COMPLETED, FAILED, CANCELLED and ROLLED_BACK.
func IsActive(s TaskState) bool {
	switch s {
	case StateCompleted, StateFailed, StateCancelled, StateRolledBack:
		return false
	}
	return s.Valid()
}

// RequestStatus is the Development Inbox status (requests.status) derived from the task state.
type RequestStatus string

const (
	RequestNew          RequestStatus = "new"
	RequestTriaged      RequestStatus = "triaged"
	RequestInProgress   RequestStatus = "in_progress"
	RequestWaitingInput RequestStatus = "waiting_input"
	RequestReview       RequestStatus = "review"
	RequestDone         RequestStatus = "done"
	RequestRejected     RequestStatus = "rejected"
	RequestFailed       RequestStatus = "failed"
)

// RequestStatusFor maps a task state to the request status per docs/DATA_MODEL.md.
// States the doc does not name are mapped as follows: CREATED -> new (no work started),
// VALIDATING and RETRYING -> in_progress, WAITING_APPROVAL -> waiting_input (a human must act),
// ROLLED_BACK -> done.
func RequestStatusFor(s TaskState) RequestStatus {
	switch s {
	case StateCreated:
		return RequestNew
	case StateQueued, StatePreparing, StateRunning, StateValidating, StateRetrying:
		return RequestInProgress
	case StateWaitingInput, StateWaitingApproval:
		return RequestWaitingInput
	case StateReviewReady, StateApproved, StateMerging, StateReleasing:
		return RequestReview
	case StateCompleted, StateRolledBack:
		return RequestDone
	case StateFailed:
		return RequestFailed
	case StateCancelled:
		return RequestRejected
	}
	return RequestNew
}

// Progress is the SDK-facing progress summary (SdkRequest.progress in docs/API_SPEC.md).
type Progress struct {
	State   string `json:"state"`
	Label   string `json:"label"`
	Percent int    `json:"percent"`
}

// ProgressFor maps a task state to a user-facing label and percentage.
func ProgressFor(s TaskState) Progress {
	p := Progress{State: string(s)}
	switch s {
	case StateCreated:
		p.Label, p.Percent = "Received", 0
	case StateQueued:
		p.Label, p.Percent = "Queued", 5
	case StatePreparing:
		p.Label, p.Percent = "Preparing workspace", 10
	case StateRunning:
		p.Label, p.Percent = "Working on it", 40
	case StateWaitingInput:
		p.Label, p.Percent = "Waiting for your answer", 40
	case StateWaitingApproval:
		p.Label, p.Percent = "Waiting for permission", 40
	case StateValidating:
		p.Label, p.Percent = "Validating", 80
	case StateReviewReady:
		p.Label, p.Percent = "Ready for review", 90
	case StateApproved:
		p.Label, p.Percent = "Approved", 92
	case StateMerging:
		p.Label, p.Percent = "Merging", 95
	case StateReleasing:
		p.Label, p.Percent = "Releasing", 97
	case StateCompleted:
		p.Label, p.Percent = "Completed", 100
	case StateFailed:
		p.Label, p.Percent = "Failed", 100
	case StateCancelled:
		p.Label, p.Percent = "Cancelled", 100
	case StateRetrying:
		p.Label, p.Percent = "Retrying", 20
	case StateRolledBack:
		p.Label, p.Percent = "Rolled back", 100
	default:
		p.Label, p.Percent = string(s), 0
	}
	return p
}
