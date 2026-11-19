package domain

import "errors"

// Sentinel errors shared by store, core and worker. Callers should match with errors.Is.
var (
	// ErrNotFound means the requested row does not exist.
	ErrNotFound = errors.New("not found")
	// ErrIllegalTransition means the state machine forbids from -> to.
	ErrIllegalTransition = errors.New("illegal transition")
	// ErrStateConflict means the task is not in any of the states the caller expected (lost CAS race).
	ErrStateConflict = errors.New("state conflict")
	// ErrDuplicate means a unique constraint (username, endpoint, event key, ...) was violated.
	ErrDuplicate = errors.New("duplicate")
	// ErrInvalid means input failed domain validation.
	ErrInvalid = errors.New("invalid")
)
