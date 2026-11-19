package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func subscribeBody(endpoint string) map[string]any {
	return map[string]any{
		"endpoint": endpoint,
		"keys":     map[string]string{"p256dh": "BP256dhKey", "auth": "AuthSecret"},
	}
}

func TestPushSubscribeValidation(t *testing.T) {
	e := newWiredEnv(t, func(o *Options) { o.Notifier = fakeNotifier{key: "BPUB"} })
	admin, _ := roleCookies(t, e)

	bad := map[string]any{
		"http scheme":    subscribeBody("http://push.example.com/abc"),
		"no host":        subscribeBody("https:///abc"),
		"missing keys":   map[string]any{"endpoint": "https://push.example.com/abc"},
		"empty endpoint": subscribeBody(""),
	}
	for name, body := range bad {
		rec := e.do(http.MethodPost, "/api/v1/push/subscribe", body, withCookie(admin), withCSRF())
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status %d, want 400 (%s)", name, rec.Code, rec.Body.String())
		}
	}

	long := "https://push.example.com/" + strings.Repeat("a", maxEndpointBytes)
	rec := e.do(http.MethodPost, "/api/v1/push/subscribe", subscribeBody(long), withCookie(admin), withCSRF())
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("oversize endpoint: %d", rec.Code)
	}

	rec = e.do(http.MethodPost, "/api/v1/push/subscribe", subscribeBody("https://push.example.com/abc"),
		withCookie(admin), withCSRF())
	if rec.Code != http.StatusCreated {
		t.Fatalf("valid subscribe: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"endpoint":"https://push.example.com/abc"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestPushUnsubscribeOnlyOwnEndpoint(t *testing.T) {
	e := newWiredEnv(t, func(o *Options) { o.Notifier = fakeNotifier{key: "BPUB"} })
	admin, dev := roleCookies(t, e)

	ep := "https://push.example.com/owned-by-admin"
	rec := e.do(http.MethodPost, "/api/v1/push/subscribe", subscribeBody(ep), withCookie(admin), withCSRF())
	if rec.Code != http.StatusCreated {
		t.Fatalf("subscribe: %d %s", rec.Code, rec.Body.String())
	}

	// Another user's delete of the same endpoint still returns 204 and must not remove the admin's subscription.
	rec = e.do(http.MethodDelete, "/api/v1/push/subscribe", map[string]string{"endpoint": ep}, withCookie(dev), withCSRF())
	if rec.Code != http.StatusNoContent {
		t.Fatalf("foreign delete: %d", rec.Code)
	}
	rec = e.do(http.MethodDelete, "/api/v1/push/subscribe", map[string]string{"endpoint": ep}, withCookie(admin), withCSRF())
	if rec.Code != http.StatusNoContent {
		t.Fatalf("own delete: %d", rec.Code)
	}
	rec = e.do(http.MethodDelete, "/api/v1/push/subscribe", map[string]string{"endpoint": "http://x"}, withCookie(admin), withCSRF())
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid endpoint delete: %d", rec.Code)
	}
}

func TestPushTestEnqueues(t *testing.T) {
	e := newWiredEnv(t, func(o *Options) { o.Notifier = fakeNotifier{key: "BPUB"} })
	admin, _ := roleCookies(t, e)
	rec := e.do(http.MethodPost, "/api/v1/push/test", nil, withCookie(admin), withCSRF())
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"created":true`) {
		t.Fatalf("push test: %d %s", rec.Code, rec.Body.String())
	}
}

func TestNotificationsListAndMarkRead(t *testing.T) {
	e := newWiredEnv(t, func(o *Options) {})
	admin, _ := roleCookies(t, e)

	rec := e.do(http.MethodGet, "/api/v1/notifications?limit=10", nil, withCookie(admin))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"notifications":[]`) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.do(http.MethodGet, "/api/v1/notifications?limit=abc", nil, withCookie(admin))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad limit: %d", rec.Code)
	}

	rec = e.do(http.MethodPost, "/api/v1/notifications/read", map[string]any{"ids": []string{}}, withCookie(admin), withCSRF())
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"updated":0`) {
		t.Fatalf("empty read: %d %s", rec.Code, rec.Body.String())
	}

	tooMany := make([]string, maxReadIDs+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("n%d", i)
	}
	rec = e.do(http.MethodPost, "/api/v1/notifications/read", map[string]any{"ids": tooMany}, withCookie(admin), withCSRF())
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("too many ids: %d", rec.Code)
	}

	rec = e.do(http.MethodPost, "/api/v1/notifications/read", map[string]any{"ids": []string{" "}}, withCookie(admin), withCSRF())
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("blank id: %d", rec.Code)
	}
}
