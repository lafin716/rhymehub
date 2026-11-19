package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const fakeName = "fake"

// fakeMaxSleep bounds FAKE:sleep so a bad fixture cannot hang a test run.
const fakeMaxSleep = 60 * time.Second

// fakeAdapter is a deterministic agent for tests. It reads directives from the prompt, one per line:
//
//	FAKE:write <relative path> <content>   writes a file inside WorkDir
//	FAKE:question <text>                   emits RHYMEHUB_QUESTION and stops
//	FAKE:deny <tool>                       emits a permission_denied event
//	FAKE:fail                              finishes with IsError
//	FAKE:sleep <seconds>                   waits, honouring cancel and timeout
//
// Lines without the FAKE: prefix are ignored. The fake never runs a process.
type fakeAdapter struct{}

// NewFake returns the deterministic test adapter.
func NewFake() Adapter { return &fakeAdapter{} }

func (f *fakeAdapter) Name() string { return fakeName }

func (f *fakeAdapter) Detect(ctx context.Context) DetectResult {
	return DetectResult{Available: true, Version: "fake", Path: "builtin"}
}

func (f *fakeAdapter) Capabilities() Capabilities {
	return Capabilities{
		Streaming: true, Resume: true, MultiTurn: true, PermissionRequests: true, Cancel: true,
	}
}

func (f *fakeAdapter) CreateSession(ctx context.Context, opts SessionOptions) (*Session, error) {
	if err := validateOptions(opts); err != nil {
		return nil, err
	}
	id, started := opts.ResumeID, opts.ResumeID != ""
	if !started {
		var err error
		if id, err = randomID("fake-"); err != nil {
			return nil, err
		}
	}
	return &Session{ID: id, Agent: fakeName, WorkDir: opts.WorkDir, opts: opts, started: started}, nil
}

func (f *fakeAdapter) Execute(ctx context.Context, s *Session, prompt string) (<-chan Event, error) {
	if s == nil || s.Agent != fakeName {
		return nil, errors.New("adapters: not a fake session")
	}
	opts := s.options()
	runCtx, cancel := runContext(ctx, opts.Timeout)
	if err := s.begin(cancel); err != nil {
		cancel()
		return nil, err
	}
	s.markStarted()
	events := make(chan Event, eventBuffer)
	go func() {
		defer close(events)
		done := fakeRun(runCtx, s, opts, prompt, func(ev Event) { events <- ev })
		s.end()
		cancel()
		events <- done
	}()
	return events, nil
}

func (f *fakeAdapter) Respond(ctx context.Context, s *Session, text string) (<-chan Event, error) {
	return f.Execute(ctx, s, text)
}

func (f *fakeAdapter) Cancel(ctx context.Context, s *Session) error {
	if s == nil {
		return nil
	}
	s.cancelRun()
	return nil
}

func (f *fakeAdapter) Status(ctx context.Context, s *Session) (Status, error) {
	if s == nil {
		return Status{}, errors.New("adapters: nil session")
	}
	return statusOf(s), nil
}

func (f *fakeAdapter) Recover(ctx context.Context, sessionID string, opts SessionOptions) (RecoveryResult, error) {
	return recoverResult(sessionID), nil
}

// fakeRun executes the directives in prompt and returns the done event.
func fakeRun(ctx context.Context, s *Session, opts SessionOptions, prompt string, emit func(Event)) Event {
	id := s.currentID()
	emit(Event{Type: EventSession, SessionID: id})
	done := Event{Type: EventDone, SessionID: id}
	fail := func(msg string) Event {
		done.IsError = true
		done.Text = msg
		return done
	}
	for _, raw := range strings.Split(prompt, "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "FAKE:") {
			continue
		}
		verb, arg, _ := strings.Cut(strings.TrimPrefix(line, "FAKE:"), " ")
		if err := ctx.Err(); err != nil {
			return fail(runReason(err))
		}
		switch verb {
		case "write":
			rel, content, _ := strings.Cut(arg, " ")
			if err := fakeWrite(opts.WorkDir, rel, content); err != nil {
				emit(Event{Type: EventError, Text: err.Error()})
				return fail(err.Error())
			}
			input, _ := json.Marshal(map[string]string{"file_path": rel})
			emit(Event{Type: EventToolUse, Tool: "Write", Input: input})
			emit(Event{Type: EventToolResult, Tool: "Write", OK: true})
		case "question":
			q := "RHYMEHUB_QUESTION: " + arg
			emit(Event{Type: EventText, Text: q})
			done.Text = q
			return done
		case "deny":
			emit(Event{Type: EventPermissionDenied, Denials: []Denial{{Tool: arg}}})
		case "fail":
			return fail("fake failure requested")
		case "sleep":
			secs, err := strconv.ParseFloat(arg, 64)
			if err != nil || secs < 0 || time.Duration(secs*float64(time.Second)) > fakeMaxSleep {
				return fail(fmt.Sprintf("invalid FAKE:sleep argument %q", arg))
			}
			timer := time.NewTimer(time.Duration(secs * float64(time.Second)))
			select {
			case <-ctx.Done():
				timer.Stop()
				return fail(runReason(ctx.Err()))
			case <-timer.C:
			}
		}
	}
	emit(Event{Type: EventText, Text: "fake done"})
	done.Text = "fake done"
	return done
}

func runReason(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "cancelled"
}

// fakeWrite writes content to rel inside dir. Absolute paths and paths escaping dir are rejected.
func fakeWrite(dir, rel, content string) error {
	if dir == "" {
		return errors.New("fake write: no WorkDir")
	}
	if !filepath.IsLocal(rel) {
		return fmt.Errorf("fake write: path %q escapes the worktree", rel)
	}
	target := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return os.WriteFile(target, []byte(content), 0o644)
}
