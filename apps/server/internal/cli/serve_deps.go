package cli

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/lafin716/rhymehub/apps/server/internal/api"
	"github.com/lafin716/rhymehub/apps/server/internal/notify"
	"github.com/lafin716/rhymehub/apps/server/internal/scheduler"
	"github.com/lafin716/rhymehub/internal/domain"
	"github.com/lafin716/rhymehub/internal/store"
	adapters "github.com/lafin716/rhymehub/packages/agent-adapters"
)

// limitsSettingKey is where PATCH /agents/limits persists the limits so they survive a restart.
const limitsSettingKey = "scheduler.limits"

// schedControl adapts *scheduler.Scheduler to api.SchedulerControl.
type schedControl struct{ s *scheduler.Scheduler }

func (c schedControl) Drain(on bool)      { c.s.Drain(on) }
func (c schedControl) Draining() bool     { return c.s.Draining() }
func (c schedControl) ActiveWorkers() int { return c.s.ActiveWorkers() }
func (c schedControl) CurrentLimits() api.AgentLimits {
	l := c.s.CurrentLimits()
	return api.AgentLimits{Global: l.Global, DefaultPerAgent: l.DefaultPerAgent, PerAgent: l.PerAgent}
}
func (c schedControl) SetLimits(l api.AgentLimits) {
	c.s.SetLimits(scheduler.Limits{Global: l.Global, DefaultPerAgent: l.DefaultPerAgent, PerAgent: l.PerAgent})
}

// loadPersistedLimits overlays limits saved by PATCH /agents/limits on top of the config defaults.
func loadPersistedLimits(ctx context.Context, st *store.Store, base scheduler.Limits, log *slog.Logger) scheduler.Limits {
	raw, err := st.GetSetting(ctx, limitsSettingKey)
	if err != nil || strings.TrimSpace(raw) == "" {
		return base
	}
	var l api.AgentLimits
	if err := json.Unmarshal([]byte(raw), &l); err != nil {
		log.Warn("ignoring unreadable persisted scheduler limits", "err", err)
		return base
	}
	return scheduler.Limits{Global: l.Global, DefaultPerAgent: l.DefaultPerAgent, PerAgent: l.PerAgent}
}

// agentDirectory implements api.AgentDirectory over the adapter registry. Detection runs `<cli> --version`,
// so results are cached for a minute. It never reads agent credential stores.
type agentDirectory struct {
	names []string
	ttl   time.Duration

	mu      sync.Mutex
	at      time.Time
	cached  []api.AgentInfo
	refresh func(ctx context.Context, names []string) []api.AgentInfo
}

func newAgentDirectory() *agentDirectory {
	return &agentDirectory{names: []string{"claude", "codex", "fake"}, ttl: time.Minute, refresh: detectAgents}
}

func (d *agentDirectory) ListAgents(ctx context.Context) []api.AgentInfo {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cached != nil && time.Since(d.at) < d.ttl {
		return cloneAgents(d.cached)
	}
	d.cached = d.refresh(ctx, d.names)
	d.at = time.Now()
	return cloneAgents(d.cached)
}

func cloneAgents(in []api.AgentInfo) []api.AgentInfo {
	out := make([]api.AgentInfo, len(in))
	copy(out, in)
	return out
}

func detectAgents(ctx context.Context, names []string) []api.AgentInfo {
	out := make([]api.AgentInfo, 0, len(names))
	for _, n := range names {
		a, err := adapters.Get(n)
		if err != nil {
			out = append(out, api.AgentInfo{Name: n, Error: err.Error()})
			continue
		}
		det := a.Detect(ctx)
		c := a.Capabilities()
		out = append(out, api.AgentInfo{
			Name: n, Available: det.Available, Version: det.Version, Path: det.Path, Error: det.Error,
			Capabilities: api.AgentCapabilities{
				Streaming: c.Streaming, Resume: c.Resume, MultiTurn: c.MultiTurn,
				PermissionRequests: c.PermissionRequests, InteractiveApproval: c.InteractiveApproval,
				Cancel: c.Cancel, CostReporting: c.CostReporting, Models: append([]string(nil), c.Models...),
			},
		})
	}
	return out
}

// eventStore is the store surface the notification watcher reads.
type eventStore interface {
	LatestEventID(ctx context.Context) (int64, error)
	ListEventsSince(ctx context.Context, taskID string, afterID int64, limit int) ([]domain.TaskEvent, error)
	GetTask(ctx context.Context, id string) (domain.Task, error)
	GetRelease(ctx context.Context, id string) (domain.Release, error)
}

// notificationWatcher turns task events into notifications: state changes that need a human (review ready,
// waiting input/approval, failed tests/build) and published releases. It starts at the current event head, so
// a restart does not re-announce history; Enqueue dedupes by event key.
type notificationWatcher struct {
	st   eventStore
	svc  *notify.Service
	tick time.Duration
	log  *slog.Logger
}

func (w *notificationWatcher) Run(ctx context.Context) {
	cursor, err := w.st.LatestEventID(ctx)
	if err != nil {
		w.log.Error("notify watcher: read event head", "err", err)
	}
	t := time.NewTicker(w.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		cursor = w.step(ctx, cursor)
	}
}

func (w *notificationWatcher) step(ctx context.Context, cursor int64) int64 {
	for {
		events, err := w.st.ListEventsSince(ctx, "", cursor, 200)
		if err != nil {
			if ctx.Err() == nil {
				w.log.Error("notify watcher: list events", "err", err)
			}
			return cursor
		}
		for _, ev := range events {
			w.handle(ctx, ev)
			cursor = ev.ID
		}
		if len(events) < 200 {
			return cursor
		}
	}
}

func (w *notificationWatcher) handle(ctx context.Context, ev domain.TaskEvent) {
	switch ev.Type {
	case domain.EventStateChanged:
		var p struct {
			To domain.TaskState `json:"to"`
		}
		if json.Unmarshal(ev.Payload, &p) != nil || p.To == "" {
			return
		}
		t, err := w.st.GetTask(ctx, ev.TaskID)
		if err != nil {
			return
		}
		d, ok := notify.ForTaskTransition(t, p.To)
		if !ok {
			return
		}
		d.EventKey = "task-event:" + itoa(ev.ID)
		w.enqueue(ctx, d)
	case domain.EventRelease:
		var p struct {
			ReleaseID string `json:"release_id"`
		}
		if json.Unmarshal(ev.Payload, &p) != nil || p.ReleaseID == "" {
			return
		}
		rel, err := w.st.GetRelease(ctx, p.ReleaseID)
		if err != nil {
			return
		}
		d := notify.ForRelease(rel, rel.ProjectID)
		d.TaskID = ev.TaskID
		w.enqueue(ctx, d)
	}
}

func (w *notificationWatcher) enqueue(ctx context.Context, d notify.Draft) {
	if _, _, err := w.svc.EnqueueDraft(ctx, d); err != nil {
		w.log.Error("notify watcher: enqueue", "kind", d.Kind, "err", err)
	}
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
