package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lafin716/rhymehub/internal/domain"
)

// Stream timing. These are variables so tests can shorten them.
var (
	ssePollInterval      = 300 * time.Millisecond
	sseHeartbeatInterval = 15 * time.Second
)

const sseBatchLimit = 500

// sseTaskEvent is TaskEvent plus the owning project, as in docs/API_SPEC.md.
type sseTaskEvent struct {
	taskEventView
	ProjectID string `json:"projectId"`
}

// latestEventID returns the highest global task_events.id, or 0 when there are none.
func (s *Server) latestEventID(ctx context.Context) (int64, error) {
	var id int64
	err := s.st.DB().QueryRowContext(ctx, "SELECT COALESCE(MAX(id), 0) FROM task_events").Scan(&id)
	return id, err
}

// writeSSE writes one SSE message. An empty id or event leaves that field out.
func writeSSE(w http.ResponseWriter, id int64, event string, data any) error {
	body, err := json.Marshal(data)
	if err != nil {
		return err
	}
	var b strings.Builder
	if id > 0 {
		fmt.Fprintf(&b, "id: %d\n", id)
	}
	if event != "" {
		fmt.Fprintf(&b, "event: %s\n", event)
	}
	fmt.Fprintf(&b, "data: %s\n\n", body)
	_, err = w.Write([]byte(b.String()))
	return err
}

// handleEventStream serves GET /api/v1/events/stream. Events are read by polling the task_events table
// with the global event id as cursor. Last-Event-ID resumes from that id. Without it the stream starts at the
// current head, so a fresh client does not replay history.
func (s *Server) handleEventStream(w http.ResponseWriter, r *http.Request, _ *principal) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal", "streaming unsupported")
		return
	}
	ctx := r.Context()
	var cursor int64
	if raw := r.Header.Get("Last-Event-ID"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid Last-Event-ID")
			return
		}
		cursor = n
	} else {
		head, err := s.latestEventID(ctx)
		if err != nil {
			writeDomainError(w, err)
			return
		}
		cursor = head
	}

	// The stream is long-lived; clear the server's write deadline for this response only.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	if err := writeSSE(w, 0, "system", map[string]any{"draining": s.Draining(), "version": s.version}); err != nil {
		return
	}
	flusher.Flush()

	poll := time.NewTicker(ssePollInterval)
	defer poll.Stop()
	beat := time.NewTicker(sseHeartbeatInterval)
	defer beat.Stop()

	// Task and request links are stable, so they are cached for the life of the stream.
	taskCache := map[string]domain.Task{}
	lookupTask := func(id string) (domain.Task, bool) {
		if t, ok := taskCache[id]; ok {
			return t, true
		}
		t, err := s.st.GetTask(ctx, id)
		if err != nil {
			return domain.Task{}, false
		}
		taskCache[id] = t
		return t, true
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-beat.C:
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-poll.C:
			events, err := s.st.ListEventsSince(ctx, "", cursor, sseBatchLimit)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				s.log.Error("sse poll events", "err", err)
				continue
			}
			for _, ev := range events {
				projectID := ""
				task, found := lookupTask(ev.TaskID)
				if found {
					projectID = task.ProjectID
				}
				if err := writeSSE(w, ev.ID, "task_event", sseTaskEvent{taskEventView: eventView(ev), ProjectID: projectID}); err != nil {
					return
				}
				if found && ev.Type == domain.EventStateChanged {
					if err := s.writeDerivedUpdates(ctx, w, ev.ID, ev.TaskID); err != nil {
						return
					}
				}
				cursor = ev.ID
			}
			if len(events) > 0 {
				flusher.Flush()
			}
		}
	}
}

// writeDerivedUpdates sends the current task_updated and request_updated snapshots after a state change.
func (s *Server) writeDerivedUpdates(ctx context.Context, w http.ResponseWriter, eventID int64, taskID string) error {
	t, err := s.st.GetTask(ctx, taskID)
	if err != nil {
		// The task may have been removed after the event was written; skip the snapshot.
		return nil
	}
	if err := writeSSE(w, eventID, "task_updated", summaryOf(t)); err != nil {
		return err
	}
	if t.RequestID != "" {
		rq, err := s.st.GetRequest(ctx, t.RequestID)
		if err == nil {
			if err := writeSSE(w, eventID, "request_updated", requestView{Request: rq}); err != nil {
				return err
			}
		}
	}
	return nil
}
