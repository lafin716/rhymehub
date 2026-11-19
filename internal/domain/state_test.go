package domain

import (
	"errors"
	"testing"
)

func TestAllStatesAndValid(t *testing.T) {
	all := AllStates()
	if len(all) != 16 {
		t.Fatalf("AllStates() returned %d states, want 16", len(all))
	}
	seen := map[TaskState]bool{}
	for _, s := range all {
		if !s.Valid() {
			t.Errorf("%s from AllStates() is not Valid", s)
		}
		if seen[s] {
			t.Errorf("duplicate state %s", s)
		}
		seen[s] = true
	}
	for _, bad := range []TaskState{"", "UNKNOWN", "running"} {
		if bad.Valid() {
			t.Errorf("%q must not be Valid", bad)
		}
	}
	// AllStates returns a copy: mutating it must not affect the package.
	all[0] = "MUTATED"
	if !StateCreated.Valid() || AllStates()[0] != StateCreated {
		t.Fatal("AllStates must return a copy")
	}
}

func TestCanTransition(t *testing.T) {
	cases := []struct {
		name     string
		from, to TaskState
		want     bool
	}{
		{"created to queued", StateCreated, StateQueued, true},
		{"created to cancelled", StateCreated, StateCancelled, true},
		{"created to running is illegal", StateCreated, StateRunning, false},
		{"queued to preparing", StateQueued, StatePreparing, true},
		{"preparing to running", StatePreparing, StateRunning, true},
		{"running to validating", StateRunning, StateValidating, true},
		{"running to waiting input", StateRunning, StateWaitingInput, true},
		{"waiting input back to queued", StateWaitingInput, StateQueued, true},
		{"waiting input to running is illegal", StateWaitingInput, StateRunning, false},
		{"validating to review ready", StateValidating, StateReviewReady, true},
		{"review ready to approved", StateReviewReady, StateApproved, true},
		{"approved to merging", StateApproved, StateMerging, true},
		{"approved to completed is illegal", StateApproved, StateCompleted, false},
		{"merging to completed", StateMerging, StateCompleted, true},
		{"merging to releasing", StateMerging, StateReleasing, true},
		{"releasing to completed", StateReleasing, StateCompleted, true},
		{"completed to rolled back", StateCompleted, StateRolledBack, true},
		{"completed to queued is illegal", StateCompleted, StateQueued, false},
		{"failed to retrying", StateFailed, StateRetrying, true},
		{"retrying to queued", StateRetrying, StateQueued, true},
		{"cancelled is final", StateCancelled, StateQueued, false},
		{"rolled back is final", StateRolledBack, StateCompleted, false},
		{"self transition is illegal", StateRunning, StateRunning, false},
		{"unknown source", TaskState("NOPE"), StateQueued, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CanTransition(tc.from, tc.to); got != tc.want {
				t.Fatalf("CanTransition(%s, %s) = %v, want %v", tc.from, tc.to, got, tc.want)
			}
		})
	}
}

func TestCheckTransitionWrapsIllegal(t *testing.T) {
	if err := CheckTransition(StateCreated, StateQueued); err != nil {
		t.Fatalf("legal edge returned %v", err)
	}
	err := CheckTransition(StateCreated, StateRunning)
	if !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("illegal edge must wrap ErrIllegalTransition, got %v", err)
	}
}

func TestValidTransitionsMatchCanTransition(t *testing.T) {
	for _, from := range AllStates() {
		next := ValidTransitions(from)
		for _, to := range AllStates() {
			listed := false
			for _, n := range next {
				if n == to {
					listed = true
				}
			}
			if listed != CanTransition(from, to) {
				t.Errorf("ValidTransitions(%s) and CanTransition disagree on %s", from, to)
			}
		}
	}
	// Returned slice is a copy.
	n := ValidTransitions(StateCreated)
	if len(n) > 0 {
		n[0] = "MUTATED"
	}
	if !CanTransition(StateCreated, StateQueued) {
		t.Fatal("ValidTransitions must return a copy")
	}
}

func TestIsTerminal(t *testing.T) {
	terminal := map[TaskState]bool{StateCancelled: true, StateRolledBack: true}
	for _, s := range AllStates() {
		if got := IsTerminal(s); got != terminal[s] {
			t.Errorf("IsTerminal(%s) = %v, want %v", s, got, terminal[s])
		}
	}
	if IsTerminal(StateCompleted) {
		t.Error("COMPLETED is not terminal: it can move to ROLLED_BACK")
	}
}

func TestIsRunning(t *testing.T) {
	running := map[TaskState]bool{StatePreparing: true, StateRunning: true, StateValidating: true}
	for _, s := range AllStates() {
		if got := IsRunning(s); got != running[s] {
			t.Errorf("IsRunning(%s) = %v, want %v", s, got, running[s])
		}
	}
}

func TestIsActive(t *testing.T) {
	inactive := map[TaskState]bool{
		StateCompleted: true, StateFailed: true, StateCancelled: true, StateRolledBack: true,
	}
	for _, s := range AllStates() {
		if got := IsActive(s); got != !inactive[s] {
			t.Errorf("IsActive(%s) = %v, want %v", s, got, !inactive[s])
		}
	}
	if IsActive(TaskState("BOGUS")) {
		t.Error("unknown state must not be active")
	}
}

func TestRequestStatusFor(t *testing.T) {
	cases := map[TaskState]RequestStatus{
		StateCreated:         RequestNew,
		StateQueued:          RequestInProgress,
		StatePreparing:       RequestInProgress,
		StateRunning:         RequestInProgress,
		StateValidating:      RequestInProgress,
		StateRetrying:        RequestInProgress,
		StateWaitingInput:    RequestWaitingInput,
		StateWaitingApproval: RequestWaitingInput,
		StateReviewReady:     RequestReview,
		StateApproved:        RequestReview,
		StateMerging:         RequestReview,
		StateReleasing:       RequestReview,
		StateCompleted:       RequestDone,
		StateRolledBack:      RequestDone,
		StateFailed:          RequestFailed,
		StateCancelled:       RequestRejected,
	}
	for _, s := range AllStates() {
		want, ok := cases[s]
		if !ok {
			t.Fatalf("test table missing state %s", s)
		}
		if got := RequestStatusFor(s); got != want {
			t.Errorf("RequestStatusFor(%s) = %s, want %s", s, got, want)
		}
	}
}

func TestProgressFor(t *testing.T) {
	for _, s := range AllStates() {
		p := ProgressFor(s)
		if p.State != string(s) {
			t.Errorf("ProgressFor(%s).State = %q", s, p.State)
		}
		if p.Label == "" {
			t.Errorf("ProgressFor(%s) has empty label", s)
		}
		if p.Percent < 0 || p.Percent > 100 {
			t.Errorf("ProgressFor(%s).Percent = %d out of range", s, p.Percent)
		}
	}
	if p := ProgressFor(StateCompleted); p.Percent != 100 || p.Label != "Completed" {
		t.Errorf("COMPLETED progress = %+v", p)
	}
	if p := ProgressFor(TaskState("X")); p.Percent != 0 || p.Label != "X" {
		t.Errorf("unknown state progress = %+v", p)
	}
}
