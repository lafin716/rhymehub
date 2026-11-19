package cli

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/lafin716/rhymehub/apps/server/internal/api"
	"github.com/lafin716/rhymehub/apps/server/internal/notify"
	"github.com/lafin716/rhymehub/apps/server/internal/scheduler"
	"github.com/lafin716/rhymehub/internal/config"
	"github.com/lafin716/rhymehub/internal/domain"
	"github.com/lafin716/rhymehub/internal/store"
)

func TestNotificationWatcherEnqueuesReviewReadyOnce(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	paths := config.NewPaths(root)
	if err := paths.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(root, "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc, err := notify.New(notify.Options{Store: st, Paths: paths, Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.CreateProject(ctx, domain.Project{Name: "p", RepoPath: t.TempDir(), MaxConcurrent: 1})
	if err != nil {
		t.Fatal(err)
	}
	task, err := st.CreateTask(ctx, domain.Task{ProjectID: p.ID, Title: "t", Prompt: "p", Agent: "fake", Priority: 2, CreatedBy: "test"})
	if err != nil {
		t.Fatal(err)
	}
	w := &notificationWatcher{st: st, svc: svc, tick: time.Second, log: log}
	cursor, err := st.LatestEventID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, to := range []domain.TaskState{domain.StateQueued, domain.StatePreparing, domain.StateRunning,
		domain.StateValidating, domain.StateReviewReady} {
		if _, err := st.TransitionTask(ctx, task.ID, nil, to, nil); err != nil {
			t.Fatalf("-> %s: %v", to, err)
		}
	}
	cursor = w.step(ctx, cursor)
	_ = w.step(ctx, 0) // replaying history must not duplicate (event-key dedupe)
	ns, err := st.ListNotifications(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(ns) != 1 || ns[0].Kind != string(notify.KindReviewReady) || ns[0].TaskID != task.ID {
		t.Fatalf("notifications = %+v (cursor %d)", ns, cursor)
	}
}

func TestSchedControlRoundTripsLimits(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s, err := scheduler.New(scheduler.Options{Store: st, Spawn: func(context.Context, domain.Task) (int, error) { return 0, nil }})
	if err != nil {
		t.Fatal(err)
	}
	c := schedControl{s}
	c.SetLimits(api.AgentLimits{Global: 3, DefaultPerAgent: 2, PerAgent: map[string]int{"claude": 1}})
	got := c.CurrentLimits()
	if got.Global != 3 || got.DefaultPerAgent != 2 || got.PerAgent["claude"] != 1 {
		t.Fatalf("limits = %+v", got)
	}
}
