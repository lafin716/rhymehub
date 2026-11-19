package api

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lafin716/rhymehub/internal/domain"
)

// Push and notification limits (docs/API_SPEC.md).
const (
	maxEndpointBytes = 2 << 10
	maxKeyBytes      = 256
	maxPushBodyBytes = 8 << 10
	maxReadIDs       = 500
	maxNotifyIDBytes = 128
)

// pushRandomID returns a random 128-bit hex identifier.
func pushRandomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func (s *Server) handleVAPIDKey(w http.ResponseWriter, r *http.Request, _ *principal) {
	if s.notifier == nil {
		writeError(w, http.StatusServiceUnavailable, "internal", "push is not configured")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"key": s.notifier.PublicKey()})
}

type pushSubscribeBody struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256DH string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

// validPushEndpoint requires an absolute https URL no longer than maxEndpointBytes.
func validPushEndpoint(raw string) bool {
	if raw == "" || len(raw) > maxEndpointBytes {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return u.Scheme == "https" && u.Host != ""
}

func (s *Server) handlePushSubscribe(w http.ResponseWriter, r *http.Request, p *principal) {
	var body pushSubscribeBody
	if !decodeJSON(w, r, &body, maxPushBodyBytes) {
		return
	}
	if !validPushEndpoint(body.Endpoint) {
		writeError(w, http.StatusBadRequest, "bad_request", "endpoint must be an https URL of at most 2KB")
		return
	}
	if body.Keys.P256DH == "" || body.Keys.Auth == "" || len(body.Keys.P256DH) > maxKeyBytes || len(body.Keys.Auth) > maxKeyBytes {
		writeError(w, http.StatusBadRequest, "bad_request", "keys.p256dh and keys.auth are required")
		return
	}
	id, err := pushRandomID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	sub, err := s.st.UpsertPushSubscription(r.Context(), domain.PushSubscription{
		ID:        id,
		UserID:    p.User.ID,
		Endpoint:  body.Endpoint,
		P256DH:    body.Keys.P256DH,
		Auth:      body.Keys.Auth,
		CreatedAt: s.now(),
	})
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, sub)
}

type pushUnsubscribeBody struct {
	Endpoint string `json:"endpoint"`
}

// handlePushUnsubscribe removes one of the caller's own subscriptions. A subscription that belongs to someone
// else, or does not exist, gets the same 204, so the endpoint does not reveal other users' subscriptions.
func (s *Server) handlePushUnsubscribe(w http.ResponseWriter, r *http.Request, p *principal) {
	var body pushUnsubscribeBody
	if !decodeJSON(w, r, &body, maxPushBodyBytes) {
		return
	}
	if !validPushEndpoint(body.Endpoint) {
		writeError(w, http.StatusBadRequest, "bad_request", "endpoint must be an https URL of at most 2KB")
		return
	}
	ctx := r.Context()
	subs, err := s.st.ListPushSubscriptionsByUser(ctx, p.User.ID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	for _, sub := range subs {
		if sub.Endpoint == body.Endpoint {
			if err := s.st.DeletePushSubscription(ctx, body.Endpoint); err != nil {
				writeDomainError(w, err)
				return
			}
			break
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePushTest(w http.ResponseWriter, r *http.Request, p *principal) {
	if s.notifier == nil {
		writeError(w, http.StatusServiceUnavailable, "internal", "push is not configured")
		return
	}
	nonce, err := pushRandomID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	n, created, err := s.notifier.Enqueue(r.Context(), "test", "test:"+p.User.ID+":"+nonce,
		"Test notification", "Push notifications are working.", "", "", "")
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": n.ID, "created": created})
}

// notificationView is one entry of the in-app notification feed.
type notificationView struct {
	ID        string     `json:"id"`
	Kind      string     `json:"kind"`
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	URL       string     `json:"url"`
	CreatedAt time.Time  `json:"createdAt"`
	ReadAt    *time.Time `json:"readAt"`
}

func (s *Server) handleListNotifications(w http.ResponseWriter, r *http.Request, _ *principal) {
	limit, ok := parseLimit(w, r.URL.Query().Get("limit"), defaultLimit)
	if !ok {
		return
	}
	ns, err := s.st.ListNotifications(r.Context(), limit)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	out := make([]notificationView, 0, len(ns))
	for _, n := range ns {
		out = append(out, notificationView{
			ID: n.ID, Kind: n.Kind, Title: n.Title, Body: n.Body, URL: n.URL,
			CreatedAt: n.CreatedAt, ReadAt: n.ReadAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"notifications": out})
}

type markReadBody struct {
	IDs []string `json:"ids"`
}

func (s *Server) handleMarkNotificationsRead(w http.ResponseWriter, r *http.Request, _ *principal) {
	var body markReadBody
	if !decodeJSON(w, r, &body, maxPushBodyBytes) {
		return
	}
	if len(body.IDs) > maxReadIDs {
		writeError(w, http.StatusBadRequest, "bad_request", "at most 500 ids per request")
		return
	}
	for _, id := range body.IDs {
		if strings.TrimSpace(id) == "" || len(id) > maxNotifyIDBytes {
			writeError(w, http.StatusBadRequest, "bad_request", "ids must be non-empty and short")
			return
		}
	}
	if len(body.IDs) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"updated": 0})
		return
	}
	n, err := s.st.MarkNotificationsRead(r.Context(), body.IDs, s.now())
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"updated": n})
}
