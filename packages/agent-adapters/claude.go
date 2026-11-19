package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

const claudeName = "claude"

// claudeDefaultAllow is always granted. Project test/build prefixes come in via SessionOptions.AllowedTools.
var claudeDefaultAllow = []string{
	"Read", "Edit", "Write", "Glob", "Grep",
	"Bash(git status:*)", "Bash(git diff:*)", "Bash(git log:*)",
}

// claudeHardDeny can never be granted. The CLI applies deny rules before allow rules.
var claudeHardDeny = []string{
	"Bash(git push:*)", "Bash(git remote:*)", "Bash(curl:*)", "Bash(wget:*)", "Bash(ssh:*)", "Bash(sudo:*)",
	"WebFetch", "WebSearch",
}

// claudeDeniedCommands are the command words behind the Bash hard denies. A grant whose command starts
// with one of them is rejected, so the grant cannot look like it allows what the deny forbids.
var claudeDeniedCommands = [][]string{
	{"git", "push"}, {"git", "remote"}, {"curl"}, {"wget"}, {"ssh"}, {"sudo"},
}

type claudeAdapter struct{ bin string }

// NewClaude returns the claude adapter. An empty bin resolves "claude" on PATH.
func NewClaude(bin string) Adapter {
	if bin == "" {
		bin = "claude"
	}
	return &claudeAdapter{bin: bin}
}

func (a *claudeAdapter) Name() string { return claudeName }

func (a *claudeAdapter) Detect(ctx context.Context) DetectResult { return detectBinary(ctx, a.bin) }

func (a *claudeAdapter) Capabilities() Capabilities {
	return Capabilities{
		Streaming: true, Resume: true, MultiTurn: true, PermissionRequests: true,
		InteractiveApproval: false, Cancel: true, CostReporting: true,
	}
}

// CreateSession assigns a new UUID (passed as --session-id on the first turn) or adopts ResumeID.
func (a *claudeAdapter) CreateSession(ctx context.Context, opts SessionOptions) (*Session, error) {
	if err := validateOptions(opts); err != nil {
		return nil, err
	}
	if _, _, err := claudeRules(opts); err != nil {
		return nil, err
	}
	id, started := opts.ResumeID, opts.ResumeID != ""
	if !started {
		var err error
		if id, err = newUUID(); err != nil {
			return nil, err
		}
	}
	return &Session{ID: id, Agent: claudeName, WorkDir: opts.WorkDir, opts: opts, started: started}, nil
}

func (a *claudeAdapter) Execute(ctx context.Context, s *Session, prompt string) (<-chan Event, error) {
	return a.turn(ctx, s, prompt)
}

func (a *claudeAdapter) Respond(ctx context.Context, s *Session, text string) (<-chan Event, error) {
	return a.turn(ctx, s, text)
}

func (a *claudeAdapter) turn(ctx context.Context, s *Session, prompt string) (<-chan Event, error) {
	if s == nil || s.Agent != claudeName {
		return nil, errors.New("adapters: not a claude session")
	}
	id, started := s.snapshot()
	if id == "" {
		return nil, errors.New("adapters: claude session has no id")
	}
	opts := s.options()
	args, err := a.buildArgs(opts, id, started, prompt)
	if err != nil {
		return nil, err
	}
	return runCLI(ctx, runSpec{
		sess:    s,
		bin:     a.bin,
		args:    args,
		dir:     opts.WorkDir,
		env:     filterEnv(opts.Env),
		timeout: opts.Timeout,
		parser:  &claudeParser{sess: s},
		started: s.markStarted,
	})
}

// buildArgs returns the argv for one turn. The prompt follows -p, as in the documented contract.
// Variadic permission flags use the = form so they cannot consume the prompt.
func (a *claudeAdapter) buildArgs(opts SessionOptions, id string, resume bool, prompt string) ([]string, error) {
	allow, deny, err := claudeRules(opts)
	if err != nil {
		return nil, err
	}
	args := []string{"-p", safePrompt(prompt), "--output-format", "stream-json", "--verbose",
		"--permission-mode", "acceptEdits"}
	if resume {
		args = append(args, "--resume", id)
	} else {
		args = append(args, "--session-id", id)
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	args = append(args,
		"--allowedTools="+strings.Join(allow, ","),
		"--disallowedTools="+strings.Join(deny, ","),
	)
	return args, nil
}

// claudeRules returns the allow and deny rule lists for a session. Deny always contains the hard
// denies and, for each DeniedPaths entry, Read/Edit/Write rules using the CLI's absolute-path form
// (`//<abs path>/**`).
func claudeRules(opts SessionOptions) (allow, deny []string, err error) {
	allow = append([]string(nil), claudeDefaultAllow...)
	seen := make(map[string]bool, len(allow))
	for _, r := range allow {
		seen[r] = true
	}
	for _, g := range opts.AllowedTools {
		g = strings.TrimSpace(g)
		if err := checkGrant(g); err != nil {
			return nil, nil, err
		}
		if !seen[g] {
			seen[g] = true
			allow = append(allow, g)
		}
	}
	deny = append([]string(nil), claudeHardDeny...)
	for _, p := range opts.DeniedPaths {
		rule := "//" + strings.TrimPrefix(filepath.ToSlash(filepath.Clean(p)), "/") + "/**"
		for _, tool := range []string{"Read", "Edit", "Write"} {
			deny = append(deny, tool+"("+rule+")")
		}
	}
	return allow, deny, nil
}

// checkGrant rejects tool grants that are too broad, malformed, or that would allow a hard-denied command.
func checkGrant(g string) error {
	switch {
	case g == "":
		return errors.New("adapters: empty tool grant")
	case strings.ContainsAny(g, ",\r\n"):
		return fmt.Errorf("adapters: tool grant %q must be a single rule", g)
	case g == "*", g == "Bash", g == "Bash(*)", g == "WebFetch", g == "WebSearch":
		return fmt.Errorf("adapters: tool grant %q is too broad", g)
	}
	for _, d := range claudeHardDeny {
		if g == d {
			return fmt.Errorf("adapters: tool %q is hard-denied", g)
		}
	}
	if strings.HasPrefix(g, "Bash(") && strings.HasSuffix(g, ")") {
		inner := strings.TrimSuffix(strings.TrimSpace(g[len("Bash("):len(g)-1]), ":*")
		words := strings.Fields(inner)
		if len(words) == 0 || strings.HasPrefix(words[0], "*") {
			return fmt.Errorf("adapters: tool grant %q is too broad", g)
		}
		for _, cmd := range claudeDeniedCommands {
			if len(words) >= len(cmd) && equalWords(words[:len(cmd)], cmd) {
				return fmt.Errorf("adapters: tool %q is hard-denied", g)
			}
		}
	}
	return nil
}

func equalWords(a, b []string) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (a *claudeAdapter) Cancel(ctx context.Context, s *Session) error {
	if s == nil {
		return nil
	}
	s.cancelRun()
	return nil
}

func (a *claudeAdapter) Status(ctx context.Context, s *Session) (Status, error) {
	if s == nil {
		return Status{}, errors.New("adapters: nil session")
	}
	return statusOf(s), nil
}

func (a *claudeAdapter) Recover(ctx context.Context, sessionID string, opts SessionOptions) (RecoveryResult, error) {
	return recoverResult(sessionID), nil
}

// --- stream-json parser ------------------------------------------------------

type claudeEnvelope struct {
	Type              string                       `json:"type"`
	Subtype           string                       `json:"subtype"`
	SessionID         string                       `json:"session_id"`
	Message           json.RawMessage              `json:"message"`
	IsError           bool                         `json:"is_error"`
	Result            string                       `json:"result"`
	TotalCostUSD      float64                      `json:"total_cost_usd"`
	Usage             claudeUsage                  `json:"usage"`
	NumTurns          int                          `json:"num_turns"`
	PermissionDenials []map[string]json.RawMessage `json:"permission_denials"`
}

type claudeUsage struct {
	InputTokens         int `json:"input_tokens"`
	CacheCreationTokens int `json:"cache_creation_input_tokens"`
	CacheReadTokens     int `json:"cache_read_input_tokens"`
	OutputTokens        int `json:"output_tokens"`
}

type claudeBlock struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	IsError bool            `json:"is_error"`
	Content json.RawMessage `json:"content"`
}

type claudeParser struct {
	sess   *Session
	result *Event // set by the result line; delivered by final
}

func (p *claudeParser) handle(line []byte) []Event {
	var env claudeEnvelope
	if err := json.Unmarshal(line, &env); err != nil {
		return nil
	}
	switch env.Type {
	case "system":
		if env.Subtype == "init" && env.SessionID != "" {
			p.sess.setID(env.SessionID)
			return []Event{{Type: EventSession, SessionID: env.SessionID}}
		}
	case "assistant", "user":
		return claudeMessageEvents(env.Type, env.Message)
	case "result":
		return p.resultEvents(env)
	}
	return nil
}

func claudeMessageEvents(kind string, raw json.RawMessage) []Event {
	var msg struct {
		Content []claudeBlock `json:"content"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &msg) != nil {
		return nil
	}
	var out []Event
	for _, b := range msg.Content {
		switch b.Type {
		case "text":
			if kind == "assistant" && b.Text != "" {
				out = append(out, Event{Type: EventText, Text: b.Text})
			}
		case "tool_use":
			out = append(out, Event{Type: EventToolUse, Tool: b.Name, Input: b.Input})
		case "tool_result":
			out = append(out, Event{Type: EventToolResult, OK: !b.IsError, Text: claudeBlockText(b.Content)})
		}
	}
	return out
}

// claudeBlockText accepts a tool_result content that is either a string or an array of text blocks.
func claudeBlockText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	texts := make([]string, 0, len(parts))
	for _, p := range parts {
		if p.Text != "" {
			texts = append(texts, p.Text)
		}
	}
	return strings.Join(texts, "\n")
}

func (p *claudeParser) resultEvents(env claudeEnvelope) []Event {
	denials := parseDenials(env.PermissionDenials)
	tokens := env.Usage.InputTokens + env.Usage.CacheCreationTokens + env.Usage.CacheReadTokens
	usage := Event{
		Type: EventUsage, CostUSD: env.TotalCostUSD, InputTokens: tokens,
		OutputTokens: env.Usage.OutputTokens, Turns: env.NumTurns,
	}
	done := usage
	done.Type = EventDone
	done.Text = env.Result
	done.SessionID = env.SessionID
	done.IsError = env.IsError || (env.Subtype != "" && env.Subtype != "success")
	done.Denials = denials
	p.result = &done
	out := []Event{usage}
	if len(denials) > 0 {
		out = append(out, Event{Type: EventPermissionDenied, Denials: denials})
	}
	return out
}

// parseDenials reads permission_denials entries. The field names are accepted in two spellings
// because the exact shape is only partly verified.
func parseDenials(entries []map[string]json.RawMessage) []Denial {
	var out []Denial
	for _, e := range entries {
		d := Denial{Input: firstRaw(e, "tool_input", "input")}
		var name string
		if raw := firstRaw(e, "tool_name", "tool", "name"); raw != nil {
			_ = json.Unmarshal(raw, &name)
		}
		d.Tool = name
		out = append(out, d)
	}
	return out
}

func firstRaw(m map[string]json.RawMessage, keys ...string) json.RawMessage {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			return v
		}
	}
	return nil
}

func (p *claudeParser) final(exitErr error, reason, stderr string) Event {
	ev := Event{Type: EventDone, SessionID: p.sess.currentID()}
	if p.result != nil {
		ev = *p.result
	}
	if reason != "" {
		ev.IsError = true
		ev.Text = reason
		return ev
	}
	if exitErr != nil || p.result == nil {
		ev.IsError = true
		if ev.Text == "" {
			ev.Text = exitMessage(exitErr, stderr)
		}
	}
	return ev
}
