// Package adapters wraps the official coding-agent CLIs (claude, codex) and a
// deterministic fake agent behind one normalized event stream. See
// docs/AGENT_RUNTIME.md for the contract.
package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Capabilities describes what an adapter honestly supports.
type Capabilities struct {
	Streaming, Resume, MultiTurn, PermissionRequests, InteractiveApproval, Cancel, CostReporting bool
	Models                                                                                       []string
}

// SessionOptions configures a session. Env must already be scrubbed by the caller; the adapter
// additionally applies its own allowlist filter (see filterEnv).
type SessionOptions struct {
	TaskID, WorkDir, Model string
	ResumeID               string   // existing session/thread id (empty = new)
	AllowedTools           []string // extra tool permissions granted for this task
	DeniedPaths            []string // absolute paths the agent must not read (secrets dir, home config)
	Env                    []string // full scrubbed environment for the CLI process
	Timeout                time.Duration
}

// Session is a handle to one agent conversation. Use Status or the session event to read its ID;
// the ID is filled in on the first session event for agents that assign IDs at runtime (codex).
type Session struct {
	ID      string
	Agent   string
	WorkDir string

	mu      sync.Mutex
	opts    SessionOptions
	started bool // the CLI already knows this session (next turn must resume it)
	run     *activeRun
}

type activeRun struct {
	cancel context.CancelFunc
	pid    int
}

// EventType enumerates normalized agent events.
type EventType string

const (
	EventSession          EventType = "session"
	EventText             EventType = "text"
	EventToolUse          EventType = "tool_use"
	EventToolResult       EventType = "tool_result"
	EventError            EventType = "error"
	EventUsage            EventType = "usage"
	EventPermissionDenied EventType = "permission_denied"
	EventDone             EventType = "done"
)

// Event is one normalized event. Only the fields relevant to Type are set.
type Event struct {
	Type      EventType
	Text      string
	Tool      string
	Input     json.RawMessage
	OK        bool
	SessionID string
	CostUSD   float64
	// InputTokens includes cache read/creation tokens when the CLI reports them separately.
	InputTokens, OutputTokens, Turns int
	Denials                          []Denial
	IsError                          bool
	Raw                              json.RawMessage
}

// Denial is a tool use the CLI refused because no permission rule allowed it.
type Denial struct {
	Tool  string
	Input json.RawMessage
}

// Status is the live state of a session.
type Status struct {
	SessionID string
	Running   bool
	PID       int
}

// RecoveryResult reports whether an interrupted session can be resumed.
type RecoveryResult struct {
	Resumable bool
	SessionID string
	Reason    string
}

// DetectResult describes whether the CLI is installed and runnable.
type DetectResult struct {
	Available bool
	Version   string
	Path      string
	Error     string
}

// Adapter is implemented by every agent backend.
type Adapter interface {
	Name() string
	Detect(ctx context.Context) DetectResult
	Capabilities() Capabilities
	CreateSession(ctx context.Context, opts SessionOptions) (*Session, error)
	// Execute runs one turn. The returned channel receives events and is closed after exactly one
	// "done" event. Consumers must drain the channel until it is closed.
	Execute(ctx context.Context, s *Session, prompt string) (<-chan Event, error)
	// Respond runs a follow-up turn on an existing session (resume).
	Respond(ctx context.Context, s *Session, text string) (<-chan Event, error)
	// Cancel kills the process group of a live turn. No-op when nothing is running.
	Cancel(ctx context.Context, s *Session) error
	Status(ctx context.Context, s *Session) (Status, error)
	Recover(ctx context.Context, sessionID string, opts SessionOptions) (RecoveryResult, error)
}

// ErrSessionBusy is returned when a turn is started while another turn on the same session runs.
var ErrSessionBusy = errors.New("adapters: session already has a running turn")

// ErrUnknownAgent is returned by Get for an unregistered name.
var ErrUnknownAgent = errors.New("adapters: unknown agent")

// Get returns the default adapter for a registry name: "claude", "codex" or "fake".
// Binaries are resolved via PATH; use NewClaude/NewCodex to pin an explicit path.
func Get(name string) (Adapter, error) {
	switch name {
	case "claude":
		return NewClaude(""), nil
	case "codex":
		return NewCodex(""), nil
	case "fake":
		return NewFake(), nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownAgent, name)
	}
}

// detectBinary resolves bin on PATH and runs `<bin> --version`.
func detectBinary(ctx context.Context, bin string) DetectResult {
	path, err := exec.LookPath(bin)
	if err != nil {
		return DetectResult{Available: false, Error: err.Error()}
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").CombinedOutput()
	if err != nil {
		return DetectResult{Available: false, Path: path, Error: fmt.Sprintf("--version failed: %v", err)}
	}
	version := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	return DetectResult{Available: true, Version: version, Path: path}
}

// ParseQuestion looks for a line `RHYMEHUB_QUESTION: <question>` in agent text (per the prompt
// protocol in docs/AGENT_RUNTIME.md section 3). It returns the first such question.
func ParseQuestion(text string) (string, bool) {
	const marker = "RHYMEHUB_QUESTION:"
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, marker) {
			q := strings.TrimSpace(strings.TrimPrefix(t, marker))
			if q != "" {
				return q, true
			}
		}
	}
	return "", false
}
