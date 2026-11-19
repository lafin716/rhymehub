package adapters

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	maxLineBytes = 4 << 20 // longest CLI output line accepted; longer lines fail the turn
	stderrKeep   = 8 << 10 // stderr bytes kept for error messages
	eventBuffer  = 64
	killGrace    = 5 * time.Second // SIGTERM to SIGKILL escalation
	waitDelay    = 5 * time.Second // max wait for pipes after the CLI exits
)

// envAllow is the environment allowlist for agent processes. Everything else is dropped, which
// removes GIT_ASKPASS, GH_TOKEN, GITHUB_TOKEN, *_TOKEN, *_SECRET, *_KEY, AWS_* and similar.
var envAllow = map[string]bool{
	"PATH": true, "HOME": true, "USER": true, "LANG": true, "TERM": true,
	"TMPDIR": true, "SHELL": true,
	// CLI config dirs only (paths to the user's own official login state, never token values).
	"CLAUDE_CONFIG_DIR": true, "CODEX_HOME": true,
}

// lineParser turns one CLI output format into normalized events.
type lineParser interface {
	// handle returns the events for one output line. Non-JSON and unknown lines return nil.
	handle(line []byte) []Event
	// final builds the terminal done event. reason is non-empty for cancel, timeout or output errors.
	final(exitErr error, reason, stderr string) Event
}

type runSpec struct {
	sess    *Session
	bin     string
	args    []string
	dir     string
	env     []string
	timeout time.Duration
	parser  lineParser
	started func() // called once the process has started
}

func runContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout > 0 {
		return context.WithTimeout(ctx, timeout)
	}
	return context.WithCancel(ctx)
}

// runCLI starts one CLI turn in its own process group and streams normalized events. The returned
// channel receives exactly one done event as its last value and is then closed.
func runCLI(ctx context.Context, spec runSpec) (<-chan Event, error) {
	runCtx, cancel := runContext(ctx, spec.timeout)
	if err := spec.sess.begin(cancel); err != nil {
		cancel()
		return nil, err
	}
	cmd := exec.Command(spec.bin, spec.args...)
	cmd.Dir = spec.dir
	cmd.Env = spec.env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = waitDelay
	var stderr tailBuffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		spec.sess.end()
		cancel()
		return nil, fmt.Errorf("adapters: start %s: %w", spec.bin, err)
	}
	pid := cmd.Process.Pid
	spec.sess.attach(pid)
	if spec.started != nil {
		spec.started()
	}

	exited := make(chan struct{})
	go func() {
		select {
		case <-runCtx.Done():
			select {
			case <-exited:
				return
			default:
			}
			terminateGroup(pid, exited)
		case <-exited:
		}
	}()

	events := make(chan Event, eventBuffer)
	go func() {
		defer close(events)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64*1024), maxLineBytes)
		for sc.Scan() {
			for _, ev := range spec.parser.handle(sc.Bytes()) {
				events <- ev
			}
		}
		scanErr := sc.Err()
		if scanErr != nil {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
		waitErr := cmd.Wait()
		close(exited)

		reason := ""
		switch {
		case errors.Is(scanErr, bufio.ErrTooLong):
			reason = "agent output line exceeded the size limit"
		case scanErr != nil:
			reason = "reading agent output: " + scanErr.Error()
		case errors.Is(runCtx.Err(), context.DeadlineExceeded):
			reason = "timeout"
		case runCtx.Err() != nil:
			reason = "cancelled"
		}
		// Clear the run slot before the done event so the caller can start the next turn at once.
		spec.sess.end()
		cancel()
		events <- spec.parser.final(waitErr, reason, stderr.String())
	}()
	return events, nil
}

// terminateGroup asks the process group to exit, then kills it if it has not exited within killGrace.
func terminateGroup(pid int, exited <-chan struct{}) {
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	t := time.NewTimer(killGrace)
	defer t.Stop()
	select {
	case <-exited:
		return
	case <-t.C:
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

// tailBuffer keeps the last stderrKeep bytes written to it.
type tailBuffer struct{ buf []byte }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - stderrKeep; over > 0 {
		t.buf = append([]byte(nil), t.buf[over:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return string(t.buf) }

// exitMessage describes a failed CLI run for the done event.
func exitMessage(exitErr error, stderr string) string {
	msg := "agent exited without a result"
	if exitErr != nil {
		msg = exitErr.Error()
	}
	if t := strings.TrimSpace(stderr); t != "" {
		msg += ": " + t
	}
	return msg
}

// --- session run bookkeeping -------------------------------------------------

func (s *Session) begin(cancel context.CancelFunc) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run != nil {
		return ErrSessionBusy
	}
	s.run = &activeRun{cancel: cancel}
	return nil
}

func (s *Session) attach(pid int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run != nil {
		s.run.pid = pid
	}
}

func (s *Session) end() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.run = nil
}

// cancelRun cancels the live turn, if any. The turn's channel still delivers a final done event.
func (s *Session) cancelRun() {
	s.mu.Lock()
	r := s.run
	s.mu.Unlock()
	if r != nil && r.cancel != nil {
		r.cancel()
	}
}

func (s *Session) snapshot() (id string, started bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ID, s.started
}

func (s *Session) currentID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ID
}

// setID records the CLI-assigned id when none is known yet.
func (s *Session) setID(id string) {
	if id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ID == "" {
		s.ID = id
	}
}

func (s *Session) markStarted() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.started = true
}

func (s *Session) options() SessionOptions {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.opts
}

func statusOf(s *Session) Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{SessionID: s.ID, Running: s.run != nil}
	if s.run != nil {
		st.PID = s.run.pid
	}
	return st
}

// --- helpers -----------------------------------------------------------------

// validateOptions checks the fields every adapter relies on.
func validateOptions(opts SessionOptions) error {
	if !filepath.IsAbs(opts.WorkDir) {
		return fmt.Errorf("adapters: WorkDir must be an absolute path, got %q", opts.WorkDir)
	}
	fi, err := os.Stat(opts.WorkDir)
	if err != nil {
		return fmt.Errorf("adapters: WorkDir: %w", err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("adapters: WorkDir %q is not a directory", opts.WorkDir)
	}
	if strings.HasPrefix(opts.Model, "-") || strings.ContainsAny(opts.Model, " \t\n") {
		return fmt.Errorf("adapters: invalid model name %q", opts.Model)
	}
	if strings.HasPrefix(opts.ResumeID, "-") || strings.ContainsAny(opts.ResumeID, " \t\n") {
		return fmt.Errorf("adapters: invalid resume id %q", opts.ResumeID)
	}
	for _, p := range opts.DeniedPaths {
		if !filepath.IsAbs(p) || filepath.Clean(p) == "/" {
			return fmt.Errorf("adapters: denied path must be a specific absolute path, got %q", p)
		}
	}
	if opts.Timeout < 0 {
		return errors.New("adapters: negative timeout")
	}
	return nil
}

// safePrompt keeps a prompt that starts with "-" from being parsed as a CLI flag.
func safePrompt(p string) string {
	if strings.HasPrefix(p, "-") {
		return "\n" + p
	}
	return p
}

// filterEnv applies the allowlist to env. A nil env means the current process environment.
func filterEnv(env []string) []string {
	if env == nil {
		env = os.Environ()
	}
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if envAllow[k] || strings.HasPrefix(k, "LC_") {
			out = append(out, kv)
		}
	}
	return out
}

// recoverResult reports whether an interrupted session can be resumed by its id. The CLI itself
// decides at resume time whether the session still exists.
func recoverResult(sessionID string) RecoveryResult {
	if sessionID == "" {
		return RecoveryResult{Reason: "no session id recorded"}
	}
	return RecoveryResult{Resumable: true, SessionID: sessionID, Reason: "resume by session id"}
}

// newUUID returns a random RFC 4122 version 4 UUID.
func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
}

// randomID returns prefix followed by 16 random hex characters.
func randomID(prefix string) (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b[:]), nil
}
