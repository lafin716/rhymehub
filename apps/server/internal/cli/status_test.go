package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStatusRemoteParsesAndSendsToken(t *testing.T) {
	const token = "rht_test_value_not_real_0000"
	t.Setenv("RHYMEHUB_TOKEN", token)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/system/status" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"no"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"1.2.3","schemaVersion":4,"uptimeSec":42,"draining":false,"activeWorkers":2,
			"supervisor":{"reachable":true,"version":"0.9.0","currentVersion":"0.9.0","updateInProgress":false}}`))
	}))
	defer srv.Close()

	env, out, errb := testEnv(t)
	if code := runStatus(context.Background(), env, []string{"--server", srv.URL}); code != ExitOK {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	s := out.String()
	for _, want := range []string{"version:      1.2.3", "schema:       4", "workers:      2 active", "reachable=true"} {
		if !strings.Contains(s, want) {
			t.Fatalf("output missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, token) {
		t.Fatal("token printed")
	}
}

func TestStatusRemoteServerErrorIsFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	env, _, errb := testEnv(t)
	if code := runStatus(context.Background(), env, []string{"--server", srv.URL}); code != ExitFailure {
		t.Fatalf("exit %d, want %d (%s)", code, ExitFailure, errb.String())
	}
}

func TestStatusRemoteRejectsBadURL(t *testing.T) {
	env, _, _ := testEnv(t)
	if code := runStatus(context.Background(), env, []string{"--server", "ftp://host"}); code != ExitUsage {
		t.Fatalf("exit %d, want %d", code, ExitUsage)
	}
}

func TestStatusLocalSummarizesEmptyDatabase(t *testing.T) {
	env, out, errb := initTestHome(t)
	if code := runStatus(context.Background(), env, nil); code != ExitOK {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	s := out.String()
	for _, want := range []string{"projects:  0", "active tasks: 0", "queued:    0", "running:   0"} {
		if !strings.Contains(s, want) {
			t.Fatalf("output missing %q:\n%s", want, s)
		}
	}
}

func TestStatusLocalWithoutDatabaseFails(t *testing.T) {
	env, _, errb := testEnv(t)
	if code := runStatus(context.Background(), env, nil); code != ExitFailure {
		t.Fatalf("exit %d, want %d", code, ExitFailure)
	}
	if !strings.Contains(errb.String(), "run rhymehub init first") {
		t.Fatalf("stderr %q", errb.String())
	}
}
