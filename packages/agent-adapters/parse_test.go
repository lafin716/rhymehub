package adapters

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// feedFixture runs every line of testdata/<file> through the parser and returns the streamed events.
// The terminal done event comes from final, as it does in runCLI.
func feedFixture(t *testing.T, p lineParser, file string) []Event {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		t.Fatal(err)
	}
	var out []Event
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, p.handle([]byte(line))...)
	}
	return out
}

func hasEvent(evs []Event, typ EventType, text string) bool {
	for _, ev := range evs {
		if ev.Type == typ && ev.Text == text {
			return true
		}
	}
	return false
}

func argWith(args []string, prefix string) string {
	for _, a := range args {
		if strings.HasPrefix(a, prefix) {
			return a
		}
	}
	return ""
}

func contains(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// --- claude stream-json ------------------------------------------------------

func TestClaudePongFixture(t *testing.T) {
	sess := &Session{}
	p := &claudeParser{sess: sess}
	evs := feedFixture(t, p, "claude_pong.jsonl")
	done := p.final(nil, "", "")

	if sess.ID != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("session id = %q", sess.ID)
	}
	if !hasEvent(evs, EventText, "PONG") {
		t.Errorf("missing text event PONG: %+v", evs)
	}
	if done.Type != EventDone || done.IsError || done.Text != "PONG" || done.Turns != 1 {
		t.Errorf("done = %+v", done)
	}
}

// testdata/claude_denied.jsonl is SYNTHETIC: it follows the stream-json shape but was written by hand,
// because no real permission denial was captured.
func TestClaudeDeniedFixtureSynthetic(t *testing.T) {
	sess := &Session{}
	p := &claudeParser{sess: sess}
	evs := feedFixture(t, p, "claude_denied.jsonl")
	done := p.final(nil, "", "")

	var denied []Event
	for _, ev := range evs {
		if ev.Type == EventPermissionDenied {
			denied = append(denied, ev)
		}
	}
	if len(denied) != 1 || len(denied[0].Denials) != 1 {
		t.Fatalf("permission_denied events = %+v", denied)
	}
	if denied[0].Denials[0].Tool != "Bash" || !strings.Contains(string(denied[0].Denials[0].Input), "curl") {
		t.Errorf("denial = %+v", denied[0].Denials[0])
	}
	failedResult := false
	for _, ev := range evs {
		if ev.Type == EventToolResult && !ev.OK {
			failedResult = true
		}
	}
	if !failedResult {
		t.Errorf("expected a failed tool_result from the denied call")
	}
	if done.IsError || len(done.Denials) != 1 {
		t.Errorf("done = %+v", done)
	}
}

func TestClaudeIgnoresUnknownLines(t *testing.T) {
	p := &claudeParser{sess: &Session{}}
	for _, line := range []string{
		"not json",
		`{"type":"future_kind","x":1}`,
		`{"type":"system","subtype":"hook_started"}`,
		`{"type":"rate_limit_event"}`,
	} {
		if evs := p.handle([]byte(line)); len(evs) != 0 {
			t.Errorf("line %q produced %+v", line, evs)
		}
	}
}

func TestClaudeFinalWithoutResult(t *testing.T) {
	p := &claudeParser{sess: &Session{}}
	if done := p.final(nil, "", ""); !done.IsError {
		t.Errorf("missing result must be an error, got %+v", done)
	}
	if done := p.final(nil, "timeout", ""); !done.IsError || done.Text != "timeout" {
		t.Errorf("reason not reported: %+v", done)
	}
}

// --- codex JSONL -------------------------------------------------------------

func TestCodexPongFixture(t *testing.T) {
	sess := &Session{}
	p := &codexParser{sess: sess}
	evs := feedFixture(t, p, "codex_pong.jsonl")
	done := p.final(nil, "", "")

	if sess.ID != "01a1f2b6-bcad-79a3-8603-297e18eb42e3" {
		t.Errorf("thread id = %q", sess.ID)
	}
	if !hasEvent(evs, EventText, "PONG") {
		t.Errorf("missing text event PONG: %+v", evs)
	}
	// Config warnings arrive as non-fatal error items; they must not fail the turn.
	if done.Type != EventDone || done.IsError || done.Text != "PONG" || done.OutputTokens != 6 {
		t.Errorf("done = %+v", done)
	}
}

func TestCodexTurnFailed(t *testing.T) {
	p := &codexParser{sess: &Session{}}
	evs := p.handle([]byte(`{"type":"turn.failed","error":{"message":"boom"}}`))
	if !hasEvent(evs, EventError, "boom") {
		t.Errorf("turn.failed not reported: %+v", evs)
	}
	if done := p.final(nil, "", ""); !done.IsError || done.Text != "boom" {
		t.Errorf("done = %+v", done)
	}
}

func TestCodexWithoutTurnCompleted(t *testing.T) {
	p := &codexParser{sess: &Session{}}
	if done := p.final(nil, "", ""); !done.IsError {
		t.Errorf("missing turn.completed must be an error, got %+v", done)
	}
}

func TestCodexIgnoresUnknownLines(t *testing.T) {
	p := &codexParser{sess: &Session{}}
	for _, line := range []string{"garbage", `{"type":"future"}`, `{"type":"item.completed","item":{"type":"reasoning"}}`} {
		if evs := p.handle([]byte(line)); len(evs) != 0 {
			t.Errorf("line %q produced %+v", line, evs)
		}
	}
}

// --- argument builders -------------------------------------------------------

func TestClaudeBuildArgsNewSession(t *testing.T) {
	a := &claudeAdapter{bin: "claude"}
	opts := SessionOptions{
		WorkDir:      "/tmp",
		DeniedPaths:  []string{"/Users/x/.rhymehub/secrets"},
		AllowedTools: []string{"Bash(go test:*)"},
	}
	args, err := a.buildArgs(opts, "11111111-2222-3333-4444-555555555555", false, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if args[0] != "-p" || args[1] != "hello" {
		t.Errorf("prompt must follow -p: %v", args[:2])
	}
	if !contains(args, "--session-id") || contains(args, "--resume") {
		t.Errorf("new session must use --session-id: %v", args)
	}
	if !strings.Contains(argWith(args, "--allowedTools="), "Bash(go test:*)") {
		t.Errorf("allow grant missing: %v", args)
	}
	deny := argWith(args, "--disallowedTools=")
	for _, want := range []string{"Bash(git push:*)", "Read(//Users/x/.rhymehub/secrets/**)"} {
		if !strings.Contains(deny, want) {
			t.Errorf("deny list missing %q: %s", want, deny)
		}
	}
}

func TestClaudeBuildArgsResume(t *testing.T) {
	a := &claudeAdapter{bin: "claude"}
	args, err := a.buildArgs(SessionOptions{WorkDir: "/tmp"}, "sess-1", true, "hi")
	if err != nil {
		t.Fatal(err)
	}
	if !contains(args, "--resume") || contains(args, "--session-id") {
		t.Errorf("resume must use --resume: %v", args)
	}
}

func TestClaudeCheckGrant(t *testing.T) {
	bad := []string{
		"", "*", "Bash", "Bash(*)", "WebFetch", "WebSearch", "Read,Write", "Bash(git push:*)",
		"Bash(git push origin main)", "Bash(git remote add x y)", "Bash(curl:*)", "Bash(wget:*)",
		"Bash(ssh:*)", "Bash(sudo:*)", "Bash(*:*)",
	}
	for _, g := range bad {
		if err := checkGrant(g); err == nil {
			t.Errorf("checkGrant(%q) accepted a rejected grant", g)
		}
	}
	for _, g := range []string{"Bash(go test:*)", "Bash(make:*)", "Edit"} {
		if err := checkGrant(g); err != nil {
			t.Errorf("checkGrant(%q) = %v", g, err)
		}
	}
}

func TestClaudeHardDenyCannotBeGranted(t *testing.T) {
	_, err := NewClaude("").CreateSession(context.Background(), SessionOptions{
		WorkDir: t.TempDir(), AllowedTools: []string{"Bash(curl:*)"},
	})
	if err == nil {
		t.Fatal("a granted hard-denied tool must fail session creation")
	}
}

func TestCodexArgs(t *testing.T) {
	opts := SessionOptions{WorkDir: "/w", Model: "m"}
	args := codexArgs(opts, "", false, "p")
	for _, want := range []string{"exec", "--json", "-C", "/w", "-s", "workspace-write", "-m", "m"} {
		if !contains(args, want) {
			t.Errorf("new turn args missing %q: %v", want, args)
		}
	}
	if args[len(args)-1] != "p" {
		t.Errorf("prompt must be last: %v", args)
	}

	args = codexArgs(opts, "tid", true, "p")
	if !contains(args, "resume") || !contains(args, "tid") || contains(args, "-C") || contains(args, "-s") {
		t.Errorf("resume turn must not use -C or -s: %v", args)
	}
	if !contains(args, "-c") || !contains(args, `sandbox_mode="workspace-write"`) {
		t.Errorf("resume turn must set the sandbox explicitly: %v", args)
	}
	if !contains(args, "--json") || !contains(args, "--skip-git-repo-check") || !contains(args, "-m") {
		t.Errorf("resume turn lost a required flag: %v", args)
	}
	if args[len(args)-1] != "p" {
		t.Errorf("prompt must be last: %v", args)
	}
}

// --- environment, detection, registry, questions ------------------------------

func TestFilterEnvDropsSecrets(t *testing.T) {
	in := []string{
		"PATH=/bin", "HOME=/h", "LC_ALL=C", "GH_TOKEN=x", "GITHUB_TOKEN=y", "GIT_ASKPASS=z",
		"AWS_SECRET_ACCESS_KEY=k", "MY_TOKEN=t", "APP_SECRET=s", "ANTHROPIC_API_KEY=k",
	}
	got := filterEnv(in)
	want := map[string]bool{"PATH=/bin": true, "HOME=/h": true, "LC_ALL=C": true}
	if len(got) != len(want) {
		t.Fatalf("filterEnv = %v", got)
	}
	for _, kv := range got {
		if !want[kv] {
			t.Errorf("unexpected variable kept: %q", kv)
		}
	}
}

func TestFilterEnvNilUsesProcessEnv(t *testing.T) {
	t.Setenv("GH_TOKEN", "should-not-leak")
	for _, kv := range filterEnv(nil) {
		if strings.HasPrefix(kv, "GH_TOKEN=") {
			t.Errorf("GH_TOKEN leaked into agent env")
		}
	}
}

func TestDetectMissingBinary(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-claude")
	res := NewClaude(missing).Detect(context.Background())
	if res.Available || res.Error == "" {
		t.Errorf("Detect(missing) = %+v", res)
	}
}

func TestGetRegistry(t *testing.T) {
	for name, want := range map[string]string{"claude": "claude", "codex": "codex", "fake": "fake"} {
		a, err := Get(name)
		if err != nil || a.Name() != want {
			t.Errorf("Get(%q) = %v, %v", name, a, err)
		}
	}
	if _, err := Get("bogus"); !errors.Is(err, ErrUnknownAgent) {
		t.Errorf("Get(bogus) err = %v", err)
	}
}

func TestParseQuestion(t *testing.T) {
	q, ok := ParseQuestion("some text\n  RHYMEHUB_QUESTION: which database? \n")
	if !ok || q != "which database?" {
		t.Errorf("ParseQuestion = %q, %v", q, ok)
	}
	if _, ok := ParseQuestion("no question here"); ok {
		t.Errorf("ParseQuestion matched plain text")
	}
	if _, ok := ParseQuestion("RHYMEHUB_QUESTION:   "); ok {
		t.Errorf("empty question must not match")
	}
}
