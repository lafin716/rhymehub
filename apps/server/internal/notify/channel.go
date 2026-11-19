package notify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/lafin716/rhymehub/internal/domain"
)

// Channel delivers one message to one subscription. Implementations must not log the subscription keys.
type Channel interface {
	Send(ctx context.Context, sub domain.PushSubscription, payload []byte) error
}

// StatusError is returned when a push service answers with a non-2xx status.
type StatusError struct {
	Code int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("push service returned HTTP %d", e.Code)
}

// IsGone reports whether the subscription no longer exists at the push service (HTTP 404 or 410). Such
// subscriptions are deleted instead of retried.
func IsGone(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && (se.Code == 404 || se.Code == 410)
}

// webPushChannel sends RFC 8291 encrypted messages with VAPID authentication (RFC 8292).
type webPushChannel struct {
	keys       vapidKeys
	subscriber string
	client     webpush.HTTPClient
	allowHTTP  bool
}

// pushTTLSeconds is how long the push service may hold an undelivered message.
const pushTTLSeconds = 3600

// pushRecordSize is the RFC 8188 record size used for payload encryption.
const pushRecordSize = 4096

// maxDrainBytes bounds how much of a push response body is read before the connection is reused.
const maxDrainBytes = 4096

func (c *webPushChannel) Send(ctx context.Context, sub domain.PushSubscription, payload []byte) error {
	if err := validateEndpoint(sub.Endpoint, c.allowHTTP); err != nil {
		return err
	}
	resp, err := webpush.SendNotificationWithContext(ctx, payload, &webpush.Subscription{
		Endpoint: sub.Endpoint,
		Keys:     webpush.Keys{Auth: sub.Auth, P256dh: sub.P256DH},
	}, &webpush.Options{
		HTTPClient:      c.client,
		RecordSize:      pushRecordSize,
		Subscriber:      c.subscriber,
		TTL:             pushTTLSeconds,
		VAPIDPublicKey:  c.keys.PublicKey,
		VAPIDPrivateKey: c.keys.PrivateKey,
	})
	if err != nil {
		return fmt.Errorf("webpush send: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrainBytes))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &StatusError{Code: resp.StatusCode}
	}
	return nil
}

// validateEndpoint rejects endpoints that are not absolute https URLs. Browsers always issue https endpoints;
// anything else would turn stored subscription rows into an outbound request primitive.
func validateEndpoint(endpoint string, allowHTTP bool) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%w: push endpoint is not a URL", domain.ErrInvalid)
	}
	if strings.EqualFold(u.Scheme, "https") || (allowHTTP && strings.EqualFold(u.Scheme, "http")) {
		return nil
	}
	return fmt.Errorf("%w: push endpoint must use https", domain.ErrInvalid)
}
