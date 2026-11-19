package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lafin716/rhymehub/internal/redact"
	"github.com/lafin716/rhymehub/internal/workspace"
)

// outputTailLimit caps the test output kept in tasks.test_output and the test_result payload.
const outputTailLimit = 16 << 10

// gitOut runs git with an explicit argv in dir. It never goes through a shell and disables prompts.
func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = workspace.GitEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimRight(stdout.String(), "\r\n"), nil
}

// headCommit returns the sha of HEAD in worktree.
func headCommit(ctx context.Context, worktree string) (string, error) {
	return gitOut(ctx, worktree, "rev-parse", "HEAD")
}

// changedFiles lists the paths that differ between base and the worktree (committed and uncommitted).
func changedFiles(ctx context.Context, worktree, base string) ([]string, error) {
	out, err := gitOut(ctx, worktree, "diff", "--name-only", base)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			files = append(files, l)
		}
	}
	return files, nil
}

// testRun is the outcome of the project's test command.
type testRun struct {
	Passed   bool
	ExitCode int
	Output   string // redacted, capped to outputTailLimit
	Duration time.Duration
}

// runTestCmd runs the configured test command with `sh -c` in dir. The command string comes only from the
// project configuration (never from request or agent text). env must already be scrubbed.
func runTestCmd(ctx context.Context, dir, command string, env []string) (testRun, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = dir
	cmd.Env = env
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	start := time.Now()
	err := cmd.Run()
	res := testRun{Duration: time.Since(start)}
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return res, fmt.Errorf("run test command: %w", err)
		}
		res.ExitCode = exitErr.ExitCode()
	}
	res.Passed = err == nil
	res.Output = outputTail(redactText(buf.String()))
	return res, nil
}

// outputTail keeps the last outputTailLimit bytes of s, starting on a UTF-8 boundary.
func outputTail(s string) string {
	if len(s) <= outputTailLimit {
		return s
	}
	s = s[len(s)-outputTailLimit:]
	for len(s) > 0 && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return s
}

// redactText removes secrets and personal data before text is stored or streamed.
func redactText(s string) string {
	out, _ := redact.Redact(s)
	return out
}
