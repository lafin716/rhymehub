package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/lafin716/rhymehub/internal/domain"
)

func TestReleaseCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	p := newProject(t, s, 1)

	rel, err := s.CreateRelease(ctx, domain.Release{
		ProjectID: p.ID, Version: "0.1.1", Tag: "v0.1.1", Strategy: "tag", Notes: "notes",
		TaskIDs: []string{"tsk_a"}, CreatedBy: "usr_1",
	})
	if err != nil {
		t.Fatalf("create release: %v", err)
	}
	if rel.Status != domain.ReleaseBuilding {
		t.Fatalf("default status = %q", rel.Status)
	}
	if _, err := s.CreateRelease(ctx, domain.Release{ProjectID: p.ID, Version: "0.1.1", CreatedBy: "x"}); !errors.Is(err, domain.ErrDuplicate) {
		t.Fatalf("duplicate version err = %v, want ErrDuplicate", err)
	}

	got, err := s.GetRelease(ctx, rel.ID)
	if err != nil {
		t.Fatalf("get release: %v", err)
	}
	if got.Version != "0.1.1" || got.Tag != "v0.1.1" || len(got.TaskIDs) != 1 || got.TaskIDs[0] != "tsk_a" || got.ApprovedBy != "" {
		t.Fatalf("round trip mismatch: %+v", got)
	}

	got.Status = domain.ReleasePublished
	got.ApprovedBy = "usr_2"
	got.Log = "ok"
	if _, err := s.UpdateRelease(ctx, got); err != nil {
		t.Fatalf("update release: %v", err)
	}
	list, err := s.ListReleases(ctx, p.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("list releases: %d %v", len(list), err)
	}
	if list[0].ApprovedBy != "usr_2" || list[0].Log != "ok" || list[0].Status != domain.ReleasePublished {
		t.Fatalf("update not persisted: %+v", list[0])
	}
	if _, err := s.GetRelease(ctx, "rel_missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing release err = %v", err)
	}
}

func TestAuditAppendAndPage(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := s.AppendAudit(ctx, domain.AuditEntry{Actor: "admin", ActorRole: "admin", Action: "task.approve",
			TargetType: "task", TargetID: "tsk", Detail: json.RawMessage(`{"n":1}`), IP: "127.0.0.1"}); err != nil {
			t.Fatalf("append audit: %v", err)
		}
	}
	if _, err := s.AppendAudit(ctx, domain.AuditEntry{}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("empty action err = %v", err)
	}
	page, err := s.ListAudit(ctx, 2, 0)
	if err != nil || len(page) != 2 || page[0].ID <= page[1].ID {
		t.Fatalf("audit page 1: %+v %v", page, err)
	}
	rest, err := s.ListAudit(ctx, 10, page[1].ID)
	if err != nil || len(rest) != 1 {
		t.Fatalf("audit page 2: %d %v", len(rest), err)
	}
}

func TestPushSubscriptions(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u := createTestUser(t, s)
	if _, err := s.UpsertPushSubscription(ctx, domain.PushSubscription{UserID: u, Endpoint: "https://push/1", P256DH: "k", Auth: "a"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := s.UpsertPushSubscription(ctx, domain.PushSubscription{UserID: u, Endpoint: "https://push/1", P256DH: "k2", Auth: "a2"}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	subs, err := s.ListPushSubscriptionsByUser(ctx, u)
	if err != nil || len(subs) != 1 || subs[0].P256DH != "k2" {
		t.Fatalf("subs = %+v %v", subs, err)
	}
	if err := s.DeletePushSubscription(ctx, "https://push/1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if subs, _ := s.ListPushSubscriptionsByUser(ctx, u); len(subs) != 0 {
		t.Fatalf("subscription not deleted")
	}
}

func TestNotificationsDedupeAndDue(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	p := newProject(t, s, 1)
	n, inserted, err := s.EnqueueNotification(ctx, domain.Notification{EventKey: "task:1:review_ready", Kind: "review_ready",
		Title: "Review ready", ProjectID: p.ID})
	if err != nil || !inserted {
		t.Fatalf("enqueue: inserted=%v err=%v", inserted, err)
	}
	again, inserted, err := s.EnqueueNotification(ctx, domain.Notification{EventKey: "task:1:review_ready", Kind: "review_ready"})
	if err != nil || inserted || again.ID != n.ID {
		t.Fatalf("dedupe: inserted=%v id=%s err=%v", inserted, again.ID, err)
	}
	// System notification with no project or task.
	if _, _, err := s.EnqueueNotification(ctx, domain.Notification{EventKey: "system:update:1", Kind: "update_succeeded"}); err != nil {
		t.Fatalf("enqueue system notification: %v", err)
	}
	due, err := s.ListDueNotifications(ctx, time.Now().Add(time.Second), 10)
	if err != nil || len(due) != 2 {
		t.Fatalf("due = %d %v", len(due), err)
	}
	if err := s.MarkNotification(ctx, n.ID, domain.NotifySent, 1, "", time.Now()); err != nil {
		t.Fatalf("mark: %v", err)
	}
	due, _ = s.ListDueNotifications(ctx, time.Now().Add(time.Second), 10)
	if len(due) != 1 {
		t.Fatalf("after mark due = %d", len(due))
	}
}

func createTestUser(t *testing.T, s *Store) string {
	t.Helper()
	u, err := s.CreateUser(context.Background(), domain.User{Username: domain.NewID("u"), PasswordHash: "x", Role: domain.Role("admin")})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return u.ID
}
