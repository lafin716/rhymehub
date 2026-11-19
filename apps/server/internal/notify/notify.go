// Package notify queues user notifications, delivers them through pluggable channels (Web Push only for now),
// and fans new notifications out to live SSE subscribers.
//
// Payloads carry only a short title, a short body, and a dashboard path. They never include secrets, diffs,
// agent output, or the VAPID private key.
package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/lafin716/rhymehub/internal/config"
	"github.com/lafin716/rhymehub/internal/domain"
)

// Store is the persistence surface the service needs. *store.Store satisfies every method except
// ListAllPushSubscriptions, which the store must add before the server can pass *store.Store here.
type Store interface {
	EnqueueNotification(ctx context.Context, n domain.Notification) (domain.Notification, bool, error)
	ListDueNotifications(ctx context.Context, now time.Time, limit int) ([]domain.Notification, error)
	MarkNotification(ctx context.Context, id string, status domain.NotifyStatus, attempts int, lastError string, nextAttempt time.Time) error
	// ListAllPushSubscriptions returns every stored Web Push subscription across users.
	ListAllPushSubscriptions(ctx context.Context) ([]domain.PushSubscription, error)
	DeletePushSubscription(ctx context.Context, endpoint string) error
}

const (
	maxTitleRunes = 120
	maxBodyRunes  = 280
	defaultTick   = 2 * time.Second
	defaultBatch  = 50
	// DefaultMaxAttempts is how many delivery passes a notification gets before it is marked failed.
	DefaultMaxAttempts = 5
	// DefaultSubscriber is the VAPID "sub" claim used when none is configured. Push services expect a contact.
	DefaultSubscriber = "mailto:rhymehub@localhost"
)

// Options configures a Service.
type Options struct {
	Store Store
	// Paths locates the secrets directory that holds the VAPID key pair.
	Paths config.Paths
	// Subscriber is the VAPID contact (mailto: or https:). Defaults to DefaultSubscriber.
	Subscriber string
	// HTTPClient performs push requests. Defaults to a client with a 15 s timeout.
	HTTPClient webpush.HTTPClient
	// Now returns the current time. Defaults to time.Now.
	Now func() time.Time
	// Tick is the delivery poll interval for Run. Defaults to 2 s.
	Tick time.Duration
	// MaxAttempts caps delivery passes per notification. Defaults to DefaultMaxAttempts.
	MaxAttempts int
	// Logger receives delivery diagnostics. Defaults to slog.Default().
	Logger *slog.Logger

	// allowHTTP permits non-https push endpoints. Tests only; production endpoints must be https.
	allowHTTP bool
}

// Service owns notification enqueueing, delivery, and SSE fan-out.
type Service struct {
	store       Store
	keys        vapidKeys
	channel     Channel
	now         func() time.Time
	tick        time.Duration
	maxAttempts int
	batch       int
	log         *slog.Logger

	mu        sync.Mutex
	subs      map[int]chan<- domain.Notification
	nextSubID int
}

// New loads the VAPID key pair from Paths.SecretsDir() (generating and saving it at mode 0600 when missing)
// and returns a Service with the Web Push channel.
func New(opts Options) (*Service, error) {
	if opts.Store == nil {
		return nil, fmt.Errorf("%w: notify store is nil", domain.ErrInvalid)
	}
	if opts.Paths.Root == "" {
		return nil, fmt.Errorf("%w: notify paths root is empty", domain.ErrInvalid)
	}
	keys, err := loadOrCreateVAPID(opts.Paths)
	if err != nil {
		return nil, err
	}
	s := &Service{
		store:       opts.Store,
		keys:        keys,
		now:         opts.Now,
		tick:        opts.Tick,
		maxAttempts: opts.MaxAttempts,
		batch:       defaultBatch,
		log:         opts.Logger,
		subs:        map[int]chan<- domain.Notification{},
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.tick <= 0 {
		s.tick = defaultTick
	}
	if s.maxAttempts <= 0 {
		s.maxAttempts = DefaultMaxAttempts
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	subscriber := opts.Subscriber
	if subscriber == "" {
		subscriber = DefaultSubscriber
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	s.channel = &webPushChannel{
		keys:       keys,
		subscriber: subscriber,
		client:     client,
		allowHTTP:  opts.allowHTTP,
	}
	return s, nil
}

// PublicKey returns the VAPID application server key the browser needs to subscribe (GET /push/vapid-public-key).
func (s *Service) PublicKey() string {
	return s.keys.PublicKey
}

// Enqueue stores a notification. eventKey is the dedupe key (for example "task:<id>:<STATE>:<version>"); when a
// notification with the same key already exists, the existing row is returned and inserted is false. New rows are
// fanned out to SSE subscribers. url must be a dashboard path starting with a single "/", or empty.
func (s *Service) Enqueue(ctx context.Context, kind, eventKey, title, body, url, projectID, taskID string) (domain.Notification, bool, error) {
	if strings.TrimSpace(kind) == "" || strings.TrimSpace(eventKey) == "" {
		return domain.Notification{}, false, fmt.Errorf("%w: notification kind and event key are required", domain.ErrInvalid)
	}
	if url != "" && (!strings.HasPrefix(url, "/") || strings.HasPrefix(url, "//")) {
		return domain.Notification{}, false, fmt.Errorf("%w: notification url must be a dashboard path", domain.ErrInvalid)
	}
	n := domain.Notification{
		EventKey:      eventKey,
		Kind:          kind,
		Title:         clip(title, maxTitleRunes),
		Body:          clip(body, maxBodyRunes),
		URL:           url,
		ProjectID:     projectID,
		TaskID:        taskID,
		Status:        domain.NotifyPending,
		NextAttemptAt: s.now().UTC(),
	}
	saved, inserted, err := s.store.EnqueueNotification(ctx, n)
	if err != nil {
		return domain.Notification{}, false, fmt.Errorf("notify: enqueue %s: %w", kind, err)
	}
	if inserted {
		s.publish(saved)
	}
	return saved, inserted, nil
}

// Subscribe registers ch to receive every newly enqueued notification. Delivery to ch is non-blocking: when the
// channel is full the event is dropped for that subscriber (SSE clients recover through the notifications list).
// The returned function unregisters ch.
func (s *Service) Subscribe(ch chan<- domain.Notification) (unsubscribe func()) {
	s.mu.Lock()
	id := s.nextSubID
	s.nextSubID++
	s.subs[id] = ch
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		delete(s.subs, id)
		s.mu.Unlock()
	}
}

func (s *Service) publish(n domain.Notification) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ch := range s.subs {
		select {
		case ch <- n:
		default:
		}
	}
}

// Run delivers due notifications until ctx is done. Each pass is logged on failure and retried on the next tick.
func (s *Service) Run(ctx context.Context) error {
	t := time.NewTicker(s.tick)
	defer t.Stop()
	for {
		if _, err := s.DeliverDue(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("notify delivery pass failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// DeliverDue runs one delivery pass over due notifications and returns how many it finished (sent, skipped,
// failed, or rescheduled). Run calls it on every tick; tests call it directly.
func (s *Service) DeliverDue(ctx context.Context) (int, error) {
	now := s.now().UTC()
	due, err := s.store.ListDueNotifications(ctx, now, s.batch)
	if err != nil {
		return 0, fmt.Errorf("notify: list due: %w", err)
	}
	if len(due) == 0 {
		return 0, nil
	}
	subs, err := s.store.ListAllPushSubscriptions(ctx)
	if err != nil {
		return 0, fmt.Errorf("notify: list subscriptions: %w", err)
	}
	gone := map[string]bool{}
	done := 0
	for _, n := range due {
		if err := ctx.Err(); err != nil {
			return done, err
		}
		if err := s.deliverOne(ctx, n, subs, gone, now); err != nil {
			return done, err
		}
		done++
	}
	return done, nil
}

// deliverOne sends n to every live subscription. Any success marks it sent. A notification with no live
// subscriptions is skipped. Transient failures are rescheduled with backoff until MaxAttempts, then failed.
func (s *Service) deliverOne(ctx context.Context, n domain.Notification, subs []domain.PushSubscription, gone map[string]bool, now time.Time) error {
	attempts := n.Attempts + 1
	msg := newMessage(n)
	delivered, live := 0, 0
	var lastErr error
	for _, sub := range subs {
		if gone[sub.Endpoint] {
			continue
		}
		live++
		err := s.channel.Send(ctx, sub, msg)
		switch {
		case err == nil:
			delivered++
		case IsGone(err):
			gone[sub.Endpoint] = true
			if derr := s.store.DeletePushSubscription(ctx, sub.Endpoint); derr != nil {
				s.log.Error("notify: delete expired subscription", "err", derr)
			}
		default:
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			lastErr = err
		}
	}
	switch {
	case delivered > 0:
		return s.store.MarkNotification(ctx, n.ID, domain.NotifySent, attempts, "", now)
	case live == 0:
		return s.store.MarkNotification(ctx, n.ID, domain.NotifySkipped, attempts, "no live push subscriptions", now)
	case lastErr == nil:
		return s.store.MarkNotification(ctx, n.ID, domain.NotifySkipped, attempts, "no live push subscriptions", now)
	}
	reason := errorReason(lastErr)
	if attempts >= s.maxAttempts {
		s.log.Warn("notify delivery failed permanently", "notification", n.ID, "attempts", attempts, "reason", reason)
		return s.store.MarkNotification(ctx, n.ID, domain.NotifyFailed, attempts, reason, now)
	}
	return s.store.MarkNotification(ctx, n.ID, domain.NotifyPending, attempts, reason, now.Add(backoff(attempts)))
}

// backoff is 30 s doubling per attempt, capped at 1 h.
func backoff(attempts int) time.Duration {
	d := 30 * time.Second
	for i := 1; i < attempts; i++ {
		d *= 2
		if d >= time.Hour {
			return time.Hour
		}
	}
	return d
}

// errorReason turns a delivery error into a short, endpoint-free string for storage.
func errorReason(err error) string {
	var se *StatusError
	if errors.As(err, &se) {
		return fmt.Sprintf("push service returned HTTP %d", se.Code)
	}
	return "push delivery failed"
}

// wirePayload is the JSON the service worker receives. Keep it to display fields only.
type wirePayload struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url"`
}

func newMessage(n domain.Notification) []byte {
	b, _ := json.Marshal(wirePayload{ID: n.ID, Kind: n.Kind, Title: n.Title, Body: n.Body, URL: n.URL})
	return b
}

func clip(s string, max int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max])
}

// --- VAPID key storage -----------------------------------------------------------------------------

const vapidFile = "vapid.json"

type vapidKeys struct {
	PublicKey  string `json:"publicKey"`
	PrivateKey string `json:"privateKey"`
}

// loadOrCreateVAPID reads $secrets/vapid.json, or generates a key pair and writes it at mode 0600.
func loadOrCreateVAPID(p config.Paths) (vapidKeys, error) {
	path := filepath.Join(p.SecretsDir(), vapidFile)
	b, err := os.ReadFile(path)
	if err == nil {
		var k vapidKeys
		if err := json.Unmarshal(b, &k); err != nil {
			return vapidKeys{}, fmt.Errorf("notify: parse %s: %w", vapidFile, err)
		}
		if k.PublicKey == "" || k.PrivateKey == "" {
			return vapidKeys{}, fmt.Errorf("notify: %s is missing a key", vapidFile)
		}
		return k, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return vapidKeys{}, fmt.Errorf("notify: read %s: %w", vapidFile, err)
	}
	priv, pub, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return vapidKeys{}, fmt.Errorf("notify: generate vapid keys: %w", err)
	}
	k := vapidKeys{PublicKey: pub, PrivateKey: priv}
	out, err := json.MarshalIndent(k, "", "  ")
	if err != nil {
		return vapidKeys{}, fmt.Errorf("notify: encode vapid keys: %w", err)
	}
	if err := writeSecretFile(path, append(out, '\n')); err != nil {
		return vapidKeys{}, err
	}
	return k, nil
}

// writeSecretFile writes data to path atomically with mode 0600 inside a 0700 directory.
func writeSecretFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("notify: create secrets dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".vapid-*.tmp")
	if err != nil {
		return fmt.Errorf("notify: temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("notify: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("notify: sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("notify: close temp: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("notify: chmod temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("notify: rename %s: %w", filepath.Base(path), err)
	}
	return nil
}
