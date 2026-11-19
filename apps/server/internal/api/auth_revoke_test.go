package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/lafin716/rhymehub/internal/config"
)

func TestPasswordResetRevokesSessions(t *testing.T) {
	e := newTestEnv(t, config.Default())
	e.setupAdmin("root")
	admin := sessionCookieFrom(t, e.login("root", testPassword))
	rec := e.do(http.MethodPost, "/api/v1/users", map[string]string{
		"username": "dev1", "password": testPassword, "role": "developer",
	}, withCookie(admin), withCSRF())
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		ID   string `json:"id"`
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	id := out.ID
	if id == "" {
		id = out.User.ID
	}
	dev := sessionCookieFrom(t, e.login("dev1", testPassword))
	if r := e.do(http.MethodGet, "/api/v1/auth/me", nil, withCookie(dev)); r.Code != http.StatusOK {
		t.Fatalf("dev session before reset: %d", r.Code)
	}
	if r := e.do(http.MethodPatch, "/api/v1/users/"+id, map[string]string{"password": testPassword + "-new"},
		withCookie(admin), withCSRF()); r.Code != http.StatusOK {
		t.Fatalf("reset: %d %s", r.Code, r.Body.String())
	}
	if r := e.do(http.MethodGet, "/api/v1/auth/me", nil, withCookie(dev)); r.Code != http.StatusUnauthorized {
		t.Fatalf("old dev session after reset: %d, want 401", r.Code)
	}
	if r := e.do(http.MethodGet, "/api/v1/auth/me", nil, withCookie(admin)); r.Code != http.StatusOK {
		t.Fatalf("admin session must survive: %d", r.Code)
	}
}
