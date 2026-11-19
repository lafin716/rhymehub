package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/lafin716/rhymehub/internal/domain"
	"github.com/lafin716/rhymehub/internal/redact"
	"github.com/lafin716/rhymehub/internal/store"
	adapters "github.com/lafin716/rhymehub/packages/agent-adapters"
	"github.com/lafin716/rhymehub/services/worker"
)

// runWorker executes one turn of a task: rhymehub worker --task ID. The scheduler claims the task (PREPARING)
// before it starts this process. The worker environment is scrubbed so agents never inherit tokens.
func runWorker(ctx context.Context, env Env, args []string) int {
	fs := flag.NewFlagSet("worker", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	taskID := fs.String("task", "", "task id to run (required)")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if *taskID == "" {
		fmt.Fprintln(env.Stderr, "worker: --task is required")
		return ExitUsage
	}
	p, err := resolvePaths(env)
	if err != nil {
		fmt.Fprintln(env.Stderr, "worker:", err)
		return ExitFailure
	}
	if _, err := os.Stat(p.DB()); err != nil {
		fmt.Fprintln(env.Stderr, "worker: database not found; run rhymehub init first")
		return ExitFailure
	}
	st, err := store.Open(p.DB())
	if err != nil {
		fmt.Fprintln(env.Stderr, "worker: open database:", err)
		return ExitFailure
	}
	defer st.Close()

	task, err := st.GetTask(ctx, *taskID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			fmt.Fprintf(env.Stderr, "worker: task %s not found\n", *taskID)
		} else {
			fmt.Fprintln(env.Stderr, "worker: load task:", err)
		}
		return ExitFailure
	}
	agent, err := adapters.Get(task.Agent)
	if err != nil {
		fmt.Fprintf(env.Stderr, "worker: agent %q: %v\n", task.Agent, err)
		return ExitFailure
	}
	err = worker.Run(ctx, worker.Options{
		Store:   st,
		Paths:   p,
		TaskID:  task.ID,
		Adapter: agent,
		Environ: redact.ScrubEnv(os.Environ(), nil),
	})
	if err != nil {
		fmt.Fprintln(env.Stderr, "worker:", err)
		return ExitFailure
	}
	return ExitOK
}
