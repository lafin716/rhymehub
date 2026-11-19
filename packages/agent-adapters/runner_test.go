package adapters

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// collect drains a turn channel until it closes. It fails the test if the turn does not finish.
func collect(t *testing.T, ch <-chan Event) []Event {
	t.Helper()
	var out []Event
	timeout := time.After(30 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-timeout:
			t.Fatal("turn did not finish within 30s")
			return out
		}
	}
}

// lastDone asserts that exactly one done event exists and that it is the last event.
func lastDone(t *testing.T, evs []Event) Event {
	t.Helper()
	count := 0
	for _, ev := range evs {
		if ev.Type == EventDone {
			count++
		}
	}
	if count != 1 || len(evs) == 0 || evs[len(evs)-1].Type != EventDone {
		t.Fatalf("want exactly one trailing done event, got %d done in %+v", count, evs)
	}
	return evs[len(evs)-1]
}

func runTurn(t *testing.T, a Adapter, s *Session, prompt string) []Event {
	t.Helper()
	ch, err := a.Execute(context.Background(), s, prompt)
	if err != nil {
		t.Fatal(err)
	}
	return collect(t, ch)
}

func newSession(t *testing.T, a Adapter, opts SessionOptions) *Session {
	t.Helper()
	if opts.WorkDir == "" {
		opts.WorkDir = t.TempDir()
	}
	s, err := a.CreateSession(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// --- fake adapter ------------------------------------------------------------

func TestFakeWriteInsideWorktree(t *testing.T) {
	dir := t.TempDir()
	a := NewFake()
	s := newSession(t, a, SessionOptions{WorkDir: dir})
	done := lastDone(t, runTurn(t, a, s, "FAKE:write out/a.txt hello"))

	got, err := os.ReadFile(filepath.Join(dir, "out", "a.txt"))
	if err != nil || string(got) != "hello" {
		t.Errorf("file = %q, %v", got, err)
	}
	if done.IsError {
		t.Errorf("done = %+v", done)
	}
}

func TestFakeWriteEscapeRejected(t *testing.T) {
	dir := t.TempDir()
	a := NewFake()
	s := newSession(t, a, SessionOptions{WorkDir: dir})
	done := lastDone(t, runTurn(t, a, s, "FAKE:write ../escape.txt x"))

	if !done.IsError {
		t.Errorf("escaping path must fail the turn: %+v", done)
	}
	if _, err := os.Stat(filepath.Join(dir, "..", "escape.txt")); err == nil {
		t.Errorf("file escaped the worktree")
	}
}

func TestFakeQuestion(t *testing.T) {
	a := NewFake()
	s := newSession(t, a, SessionOptions{})
	done := lastDone(t, runTurn(t, a, s, "FAKE:question Which branch?"))

	q, ok := ParseQuestion(done.Text)
	if !ok || q != "Which branch?" {
		t.Errorf("question = %q, %v", q, ok)
	}
}

func TestFakeDeniedEmitsPermissionEvent(t *testing.T) {
	a := NewFake()
	s := newSession(t, a, SessionOptions{})
	evs := runTurn(t, a, s, "FAKE:deny Bash")
	lastDone(t, evs)

	found := false
	for _, ev := range evs {
		if ev.Type == EventPermissionDenied && len(ev.Denials) == 1 && ev.Denials[0].Tool == "Bash" {
			found = true
		}
	}
	if !found {
		t.Errorf("no permission_denied event for Bash: %+v", evs)
	}
}

func TestFakeFail(t *testing.T) {
	a := NewFake()
	s := newSession(t, a, SessionOptions{})
	if done := lastDone(t, runTurn(t, a, s, "FAKE:fail")); !done.IsError {
		t.Errorf("done = %+v", done)
	}
}

func TestFakeSleepTimeout(t *testing.T) {
	a := NewFake()
	s := newSession(t, a, SessionOptions{Timeout: 50 * time.Millisecond})
	done := lastDone(t, runTurn(t, a, s, "FAKE:sleep 5"))
	if !done.IsError || done.Text != "timeout" {
		t.Errorf("done = %+v", done)
	}
}

func TestFakeSleepArgumentBounded(t *testing.T) {
	a := NewFake()
	s := newSession(t, a, SessionOptions{})
	if done := lastDone(t, runTurn(t, a, s, "FAKE:sleep 1000")); !done.IsError {
		t.Errorf("oversized sleep must fail: %+v", done)
	}
}

func TestFakeCancel(t *testing.T) {
	a := NewFake()
	s := newSession(t, a, SessionOptions{})
	ch, err := a.Execute(context.Background(), s, "FAKE:sleep 5")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Cancel(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	done := lastDone(t, collect(t, ch))
	if !done.IsError || done.Text != "cancelled" {
		t.Errorf("done = %+v", done)
	}
}

func TestFakeBusySession(t *testing.T) {
	a := NewFake()
	s := newSession(t, a, SessionOptions{})
	ch, err := a.Execute(context.Background(), s, "FAKE:sleep 0.3")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Execute(context.Background(), s, "FAKE:fail"); !errors.Is(err, ErrSessionBusy) {
		t.Errorf("second turn err = %v, want ErrSessionBusy", err)
	}
	collect(t, ch)
}

// --- stub CLI processes (never the real claude or codex) ----------------------

// stubClaude returns a claude adapter whose binary is a shell script running body.
func stubClaude(t *testing.T, body string) Adapter {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "claude-stub")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return NewClaude(bin)
}

func TestStubCancelKillsProcessGroup(t *testing.T) {
	a := stubClaude(t, "sleep 30")
	s := newSession(t, a, SessionOptions{})
	ch, err := a.Execute(context.Background(), s, "hi")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Cancel(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	done := lastDone(t, collect(t, ch))
	if !done.IsError || done.Text != "cancelled" {
		t.Errorf("done = %+v", done)
	}
}

func TestStubTimeout(t *testing.T) {
	a := stubClaude(t, "sleep 30")
	s := newSession(t, a, SessionOptions{Timeout: 300 * time.Millisecond})
	done := lastDone(t, runTurn(t, a, s, "hi"))
	if !done.IsError || done.Text != "timeout" {
		t.Errorf("done = %+v", done)
	}
}

func TestStubOversizedLineFailsTurn(t *testing.T) {
	a := stubClaude(t, `head -c 5000000 /dev/zero | tr '\0' 'a'; echo`)
	s := newSession(t, a, SessionOptions{})
	done := lastDone(t, runTurn(t, a, s, "hi"))
	if !done.IsError || !strings.Contains(done.Text, "size limit") {
		t.Errorf("done = %+v", done)
	}
}

func TestStubNonZeroExitReportsStderr(t *testing.T) {
	a := stubClaude(t, "echo boom >&2; exit 3")
	s := newSession(t, a, SessionOptions{})
	done := lastDone(t, runTurn(t, a, s, "hi"))
	if !done.IsError || !strings.Contains(done.Text, "boom") {
		t.Errorf("done = %+v", done)
	}
}

func TestStubBusySession(t *testing.T) {
	a := stubClaude(t, "sleep 30")
	s := newSession(t, a, SessionOptions{})
	ch, err := a.Execute(context.Background(), s, "hi")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Execute(context.Background(), s, "again"); !errors.Is(err, ErrSessionBusy) {
		t.Errorf("second turn err = %v, want ErrSessionBusy", err)
	}
	if err := a.Cancel(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	collect(t, ch)
}
