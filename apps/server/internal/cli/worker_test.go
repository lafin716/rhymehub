package cli

import (
	"context"
	"strings"
	"testing"
)

// Only failure paths are covered here. A successful run needs a PREPARING task and a real agent, which the
// services/worker tests exercise.

func TestWorkerRequiresTaskFlag(t *testing.T) {
	env, _, errb := testEnv(t)
	if code := runWorker(context.Background(), env, nil); code != ExitUsage {
		t.Fatalf("exit %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(errb.String(), "--task is required") {
		t.Fatalf("stderr %q", errb.String())
	}
}

func TestWorkerFailsWithoutDatabase(t *testing.T) {
	env, _, errb := testEnv(t)
	if code := runWorker(context.Background(), env, []string{"--task", "tsk_missing"}); code != ExitFailure {
		t.Fatalf("exit %d, want %d", code, ExitFailure)
	}
	if !strings.Contains(errb.String(), "run rhymehub init first") {
		t.Fatalf("stderr %q", errb.String())
	}
}

func TestWorkerUnknownTask(t *testing.T) {
	env, _, errb := initTestHome(t)
	if code := runWorker(context.Background(), env, []string{"--task", "tsk_does_not_exist"}); code != ExitFailure {
		t.Fatalf("exit %d, want %d", code, ExitFailure)
	}
	if !strings.Contains(errb.String(), "not found") {
		t.Fatalf("stderr %q", errb.String())
	}
}

func TestWorkerRejectsUnknownFlag(t *testing.T) {
	env, _, _ := testEnv(t)
	if code := runWorker(context.Background(), env, []string{"--bogus"}); code != ExitUsage {
		t.Fatalf("exit %d, want %d", code, ExitUsage)
	}
}
