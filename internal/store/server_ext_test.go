package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lafin716/rhymehub/internal/domain"
)

func TestListTasksByRequest(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	p := newProject(t, s, 1)
	req, err := s.CreateRequest(ctx, domain.Request{
		ProjectID: p.ID, Source: domain.RequestSource("sdk"), Type: domain.RequestType("bug"),
		Description: "crash on start",
	})
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	other, err := s.CreateRequest(ctx, domain.Request{
		ProjectID: p.ID, Source: domain.RequestSource("sdk"), Type: domain.RequestType("feature"),
		Description: "other",
	})
	if err != nil {
		t.Fatalf("create other request: %v", err)
	}

	mk := func(requestID string) domain.Task {
		task, err := s.CreateTask(ctx, domain.Task{
			ProjectID: p.ID, RequestID: requestID, Title: "t", Prompt: "p", Agent: "fake", CreatedBy: "test",
		})
		if err != nil {
			t.Fatalf("create task: %v", err)
		}
		return task
	}
	t1 := mk(req.ID)
	t2 := mk(req.ID)
	mk(other.ID)
	mk("")

	got, err := s.ListTasksByRequest(ctx, req.ID)
	if err != nil {
		t.Fatalf("list by request: %v", err)
	}
	if len(got) != 2 || got[0].ID != t1.ID || got[1].ID != t2.ID {
		t.Fatalf("ListTasksByRequest = %+v, want [%s %s]", got, t1.ID, t2.ID)
	}
	if got[0].RequestID != req.ID {
		t.Fatalf("RequestID not round-tripped: %q", got[0].RequestID)
	}

	none, err := s.ListTasksByRequest(ctx, "req_missing")
	if err != nil || len(none) != 0 {
		t.Fatalf("unknown request: got %d tasks, err %v", len(none), err)
	}
	if _, err := s.ListTasksByRequest(ctx, ""); err == nil {
		t.Fatal("empty request id must be rejected")
	}
}

func TestSDKIdempotencyLifecycle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	p := newProject(t, s, 1)
	now := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)

	if _, found, err := s.GetSDKIdempotency(ctx, p.ID, "key-1", now); err != nil || found {
		t.Fatalf("fresh key: found=%v err=%v, want not found", found, err)
	}

	if err := s.PutSDKIdempotency(ctx, p.ID, "key-1", "req_a", "hash-a", now); err != nil {
		t.Fatalf("put: %v", err)
	}
	rec, found, err := s.GetSDKIdempotency(ctx, p.ID, "key-1", now.Add(23*time.Hour))
	if err != nil || !found {
		t.Fatalf("within 24h: found=%v err=%v", found, err)
	}
	if rec.RequestID != "req_a" || rec.Hash != "hash-a" {
		t.Fatalf("record = %+v", rec)
	}

	if _, found, err := s.GetSDKIdempotency(ctx, p.ID, "key-1", now.Add(25*time.Hour)); err != nil || found {
		t.Fatalf("after 24h: found=%v err=%v, want expired", found, err)
	}

	// Scoped per project: the same key in another project is independent.
	p2 := newProject(t, s, 1)
	if _, found, err := s.GetSDKIdempotency(ctx, p2.ID, "key-1", now); err != nil || found {
		t.Fatalf("other project: found=%v err=%v, want not found", found, err)
	}

	if err := s.PutSDKIdempotency(ctx, p.ID, "key-1", "", "h", now); err == nil {
		t.Fatal("empty request id must be rejected")
	}
}

func TestLatestEventIDAndRequestPriority(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if id, err := s.LatestEventID(ctx); err != nil || id != 0 {
		t.Fatalf("empty latest = %d %v", id, err)
	}
	p := newProject(t, s, 1)
	task, err := s.CreateTask(ctx, domain.Task{ProjectID: p.ID, Title: "t", Prompt: "p", Agent: "fake", CreatedBy: "test"})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	evs, err := s.ListEventsSince(ctx, task.ID, 0, 0)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	latest, err := s.LatestEventID(ctx)
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if len(evs) > 0 && latest != evs[len(evs)-1].ID {
		t.Fatalf("latest = %d, want %d", latest, evs[len(evs)-1].ID)
	}

	req, err := s.CreateRequest(ctx, domain.Request{ProjectID: p.ID, Source: domain.RequestSource("sdk"),
		Type: domain.RequestType("bug"), Description: "d"})
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	if err := s.SetRequestPriority(ctx, req.ID, 4); err != nil {
		t.Fatalf("set priority: %v", err)
	}
	got, err := s.GetRequest(ctx, req.ID)
	if err != nil || got.Priority != 4 {
		t.Fatalf("priority = %d %v", got.Priority, err)
	}
	if err := s.SetRequestPriority(ctx, req.ID, 5); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("out of range err = %v", err)
	}
	if err := s.SetRequestPriority(ctx, "req_missing", 1); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing err = %v", err)
	}
}
