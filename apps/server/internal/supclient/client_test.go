package supclient

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const testToken = "sup-token-123"

// fakeSupervisor records requests and answers with canned responses.
type fakeSupervisor struct {
	mu       sync.Mutex
	auth     []string
	bodies   map[string]string
	calls    []string
	updateSt int
}

func (f *fakeSupervisor) handler(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	record := func(r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	}
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(f.bodies["/status"]))
	})
	mux.HandleFunc("/updates", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		_, _ = w.Write([]byte(f.bodies["/updates"]))
	})
	mux.HandleFunc("/update", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("update body: %v", err)
		}
		f.mu.Lock()
		f.bodies["/update"] = body["version"]
		code := f.updateSt
		f.mu.Unlock()
		if code == 0 {
			code = http.StatusOK
		}
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`{"error":"supervisor: an update or rollback is already in progress"}`))
	})
	mux.HandleFunc("/rollback", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"upd_1"}`))
	})
	return mux
}

func newFake(t *testing.T) (*fakeSupervisor, *Client) {
	t.Helper()
	f := &fakeSupervisor{bodies: map[string]string{}}
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	addr := strings.TrimPrefix(srv.URL, "http://")
	return f, New(addr, testToken, srv.Client())
}

func TestStatusMapsFields(t *testing.T) {
	f, c := newFake(t)
	f.bodies["/status"] = `{"currentVersion":"0.2.0","previous":"0.1.0","coreRunning":true,"corePid":42,` +
		`"coreHealthy":true,"coreVersion":"0.2.0","updateInProgress":true}`

	got := c.Status(context.Background())
	if !got.Reachable || !got.UpdateInProgress || got.Version != "0.2.0" || got.CurrentVersion != "0.2.0" {
		t.Fatalf("status = %+v", got)
	}
	if len(f.auth) != 1 || f.auth[0] != "Bearer "+testToken {
		t.Fatalf("auth header = %v", f.auth)
	}
}

func TestStatusUnreachableIsNotReachable(t *testing.T) {
	// Grab a free loopback port, then close it so nothing listens there.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	c := New(addr, testToken, nil)
	got := c.Status(context.Background())
	if got.Reachable || got.Version != "" || got.UpdateInProgress {
		t.Fatalf("unreachable status = %+v", got)
	}
}

func TestStatusBadTokenIsNotReachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	c := New(strings.TrimPrefix(srv.URL, "http://"), testToken, nil)
	if got := c.Status(context.Background()); got.Reachable {
		t.Fatalf("401 must not be reachable: %+v", got)
	}
}

func TestStatusTimeoutIsNotReachable(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	defer srv.Close()
	defer close(block)

	c := New(strings.TrimPrefix(srv.URL, "http://"), testToken, nil)
	c.statusTimeout = 50 * time.Millisecond
	start := time.Now()
	if got := c.Status(context.Background()); got.Reachable {
		t.Fatalf("hung supervisor must not be reachable: %+v", got)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("status did not honour its timeout")
	}
}

func TestUpdateHistoryReturnsUpdatesArray(t *testing.T) {
	f, c := newFake(t)
	f.bodies["/updates"] = `{"updates":[{"id":"upd_1","from":"0.1.0","to":"0.2.0","status":"succeeded"}],"inFlight":null}`

	recs, err := c.UpdateHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("records = %d", len(recs))
	}
	var rec struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(recs[0], &rec); err != nil || rec.ID != "upd_1" {
		t.Fatalf("record = %s (%v)", recs[0], err)
	}
}

func TestUpdateHistoryEmptyIsNonNil(t *testing.T) {
	f, c := newFake(t)
	f.bodies["/updates"] = `{"updates":null}`
	recs, err := c.UpdateHistory(context.Background())
	if err != nil || recs == nil || len(recs) != 0 {
		t.Fatalf("recs = %v, err = %v", recs, err)
	}
}

func TestUpdatePostsVersionWithAuth(t *testing.T) {
	f, c := newFake(t)
	f.updateSt = http.StatusOK
	if err := c.Update(context.Background(), "0.3.0"); err != nil {
		t.Fatal(err)
	}
	if f.bodies["/update"] != "0.3.0" {
		t.Fatalf("version sent = %q", f.bodies["/update"])
	}
	if f.auth[0] != "Bearer "+testToken {
		t.Fatalf("auth = %q", f.auth[0])
	}
}

func TestUpdateErrorCarriesStatusAndMessage(t *testing.T) {
	f, c := newFake(t)
	f.updateSt = http.StatusConflict
	err := c.Update(context.Background(), "0.3.0")
	var se *StatusError
	if !errors.As(err, &se) || se.Code != http.StatusConflict {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(se.Message, "already in progress") {
		t.Fatalf("message = %q", se.Message)
	}
}

func TestRollbackPostsWithAuth(t *testing.T) {
	f, c := newFake(t)
	if err := c.Rollback(context.Background(), "upd_1"); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || f.calls[0] != "POST /rollback" {
		t.Fatalf("calls = %v", f.calls)
	}
	if f.auth[0] != "Bearer "+testToken {
		t.Fatalf("auth = %q", f.auth[0])
	}
}

func TestUnreachableUpdateReturnsError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	c := New(addr, testToken, nil)
	if err := c.Update(context.Background(), "0.3.0"); err == nil {
		t.Fatal("expected error from unreachable supervisor")
	}
}
