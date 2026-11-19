package adapters

import (
	"context"
	"encoding/json"
	"errors"
)

const codexName = "codex"

type codexAdapter struct{ bin string }

// NewCodex returns the codex adapter. An empty bin resolves "codex" on PATH.
func NewCodex(bin string) Adapter {
	if bin == "" {
		bin = "codex"
	}
	return &codexAdapter{bin: bin}
}

func (a *codexAdapter) Name() string { return codexName }

func (a *codexAdapter) Detect(ctx context.Context) DetectResult { return detectBinary(ctx, a.bin) }

// Capabilities: the sandbox is enforced by Codex itself, so no interactive permission requests.
func (a *codexAdapter) Capabilities() Capabilities {
	return Capabilities{Streaming: true, Resume: true, MultiTurn: true, Cancel: true}
}

// CreateSession adopts ResumeID when given. Otherwise the thread id arrives with the first
// thread.started event and is recorded by the parser.
func (a *codexAdapter) CreateSession(ctx context.Context, opts SessionOptions) (*Session, error) {
	if err := validateOptions(opts); err != nil {
		return nil, err
	}
	return &Session{
		ID: opts.ResumeID, Agent: codexName, WorkDir: opts.WorkDir,
		opts: opts, started: opts.ResumeID != "",
	}, nil
}

func (a *codexAdapter) Execute(ctx context.Context, s *Session, prompt string) (<-chan Event, error) {
	return a.turn(ctx, s, prompt)
}

func (a *codexAdapter) Respond(ctx context.Context, s *Session, text string) (<-chan Event, error) {
	return a.turn(ctx, s, text)
}

func (a *codexAdapter) turn(ctx context.Context, s *Session, prompt string) (<-chan Event, error) {
	if s == nil || s.Agent != codexName {
		return nil, errors.New("adapters: not a codex session")
	}
	id, resume := s.snapshot()
	if resume && id == "" {
		return nil, errors.New("adapters: codex session has no thread id yet")
	}
	opts := s.options()
	return runCLI(ctx, runSpec{
		sess:    s,
		bin:     a.bin,
		args:    codexArgs(opts, id, resume, prompt),
		dir:     opts.WorkDir,
		env:     filterEnv(opts.Env),
		timeout: opts.Timeout,
		parser:  &codexParser{sess: s},
	})
}

// codexArgs builds `codex exec --json ...` or `codex exec resume <thread> --json ...`.
// Verified against codex 0.164 --help: `exec resume` has no -C or -s flags and accepts `-c key=value`.
// Resume turns therefore set the sandbox explicitly with `-c sandbox_mode="workspace-write"` instead of
// relying on the original session, and the working directory comes from cmd.Dir (the worktree).
func codexArgs(opts SessionOptions, id string, resume bool, prompt string) []string {
	args := []string{"exec"}
	if resume {
		args = append(args, "resume", id)
	}
	args = append(args, "--json")
	if resume {
		args = append(args, "-c", `sandbox_mode="workspace-write"`)
	} else {
		args = append(args, "-C", opts.WorkDir, "-s", "workspace-write")
	}
	args = append(args, "--skip-git-repo-check")
	if opts.Model != "" {
		args = append(args, "-m", opts.Model)
	}
	return append(args, safePrompt(prompt))
}

func (a *codexAdapter) Cancel(ctx context.Context, s *Session) error {
	if s == nil {
		return nil
	}
	s.cancelRun()
	return nil
}

func (a *codexAdapter) Status(ctx context.Context, s *Session) (Status, error) {
	if s == nil {
		return Status{}, errors.New("adapters: nil session")
	}
	return statusOf(s), nil
}

func (a *codexAdapter) Recover(ctx context.Context, sessionID string, opts SessionOptions) (RecoveryResult, error) {
	return recoverResult(sessionID), nil
}

// --- JSONL parser ------------------------------------------------------------

type codexLine struct {
	Type     string          `json:"type"`
	ThreadID string          `json:"thread_id"`
	Item     json.RawMessage `json:"item"`
	Usage    *codexUsage     `json:"usage"`
	Error    json.RawMessage `json:"error"`
	Message  string          `json:"message"`
}

type codexUsage struct {
	InputTokens       int `json:"input_tokens"`
	CachedInputTokens int `json:"cached_input_tokens"`
	OutputTokens      int `json:"output_tokens"`
}

type codexItem struct {
	ID               string          `json:"id"`
	Type             string          `json:"type"`
	Text             string          `json:"text"`
	Command          string          `json:"command"`
	AggregatedOutput string          `json:"aggregated_output"`
	ExitCode         *int            `json:"exit_code"`
	Status           string          `json:"status"`
	Changes          json.RawMessage `json:"changes"`
	Message          string          `json:"message"`
}

type codexParser struct {
	sess      *Session
	lastText  string
	usage     *Event // from turn.completed
	completed bool
	failure   string // from turn.failed
}

func (p *codexParser) handle(line []byte) []Event {
	var l codexLine
	if err := json.Unmarshal(line, &l); err != nil {
		return nil
	}
	switch l.Type {
	case "thread.started":
		if l.ThreadID == "" {
			return nil
		}
		p.sess.setID(l.ThreadID)
		p.sess.markStarted()
		return []Event{{Type: EventSession, SessionID: l.ThreadID}}
	case "item.started", "item.completed":
		var it codexItem
		if err := json.Unmarshal(l.Item, &it); err != nil {
			return nil
		}
		return p.item(l.Type == "item.completed", it)
	case "turn.completed":
		p.completed = true
		if l.Usage == nil {
			return nil
		}
		p.usage = &Event{
			Type:         EventUsage,
			InputTokens:  l.Usage.InputTokens,
			OutputTokens: l.Usage.OutputTokens,
			Turns:        1,
		}
		return []Event{*p.usage}
	case "turn.failed":
		p.failure = codexErrorText(l.Error, l.Message)
		return []Event{{Type: EventError, Text: p.failure}}
	case "error":
		// Top-level error lines (e.g. reconnect notices or config warnings) are non-fatal.
		return []Event{{Type: EventError, Text: codexErrorText(l.Error, l.Message)}}
	}
	return nil
}

func (p *codexParser) item(completed bool, it codexItem) []Event {
	switch it.Type {
	case "agent_message":
		if completed && it.Text != "" {
			p.lastText = it.Text
			return []Event{{Type: EventText, Text: it.Text}}
		}
	case "command_execution":
		if !completed {
			input, _ := json.Marshal(map[string]string{"command": it.Command})
			return []Event{{Type: EventToolUse, Tool: "shell", Input: input}}
		}
		ok := it.Status != "failed" && (it.ExitCode == nil || *it.ExitCode == 0)
		return []Event{{Type: EventToolResult, Tool: "shell", OK: ok, Text: it.AggregatedOutput}}
	case "file_change":
		if completed {
			return []Event{{Type: EventToolUse, Tool: "file_change", OK: it.Status != "failed", Input: it.Changes}}
		}
	case "mcp_tool_call", "web_search":
		if !completed {
			return []Event{{Type: EventToolUse, Tool: it.Type}}
		}
		return []Event{{Type: EventToolResult, Tool: it.Type, OK: it.Status != "failed"}}
	case "error":
		if completed && it.Message != "" {
			return []Event{{Type: EventError, Text: it.Message}}
		}
	}
	return nil
}

// codexErrorText extracts a message from an error value that may be an object, a string or absent.
func codexErrorText(raw json.RawMessage, fallback string) string {
	if len(raw) > 0 {
		var obj struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(raw, &obj) == nil && obj.Message != "" {
			return obj.Message
		}
		var s string
		if json.Unmarshal(raw, &s) == nil && s != "" {
			return s
		}
	}
	if fallback != "" {
		return fallback
	}
	return "codex error"
}

func (p *codexParser) final(exitErr error, reason, stderr string) Event {
	ev := Event{Type: EventDone, SessionID: p.sess.currentID(), Text: p.lastText}
	if p.usage != nil {
		ev.InputTokens = p.usage.InputTokens
		ev.OutputTokens = p.usage.OutputTokens
		ev.Turns = p.usage.Turns
	}
	switch {
	case reason != "":
		ev.IsError, ev.Text = true, reason
	case p.failure != "":
		ev.IsError, ev.Text = true, p.failure
	case exitErr != nil:
		ev.IsError, ev.Text = true, exitMessage(exitErr, stderr)
	case !p.completed:
		ev.IsError, ev.Text = true, "agent ended without turn.completed"
	}
	return ev
}
