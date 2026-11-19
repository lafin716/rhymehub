// Package supclient is the core's HTTP client for the supervisor loopback API (services/supervisor/api.go).
// It implements api.Supervisor and api.SupervisorInfo. The wire shapes are mirrored locally so this package
// does not import services/supervisor.
package supclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/lafin716/rhymehub/apps/server/internal/api"
)

// statusTimeout bounds GET /status so a hung supervisor cannot stall API requests.
const statusTimeout = 2 * time.Second

// maxResponseBytes caps supervisor responses read by the client.
const maxResponseBytes = 1 << 20

// Client talks to the supervisor at http://<addr> with a bearer token.
type Client struct {
	base          string
	token         string
	hc            *http.Client
	statusTimeout time.Duration
}

var (
	_ api.Supervisor     = (*Client)(nil)
	_ api.SupervisorInfo = (*Client)(nil)
)

// New returns a client for the supervisor at addr (for example "127.0.0.1:7781"). A nil httpClient
// selects http.DefaultClient. Update and Rollback use httpClient's timeout, so callers that run long
// updates should pass a client without a short timeout; Status always applies its own short timeout.
func New(addr, token string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{base: "http://" + addr, token: token, hc: httpClient, statusTimeout: statusTimeout}
}

// Status reports the supervisor's view of the core. Any error (unreachable, bad token, bad body) yields
// Reachable=false, so callers can show the degraded state without handling errors.
func (c *Client) Status(ctx context.Context) api.SupervisorStatus {
	ctx, cancel := context.WithTimeout(ctx, c.statusTimeout)
	defer cancel()
	var body struct {
		CurrentVersion   string `json:"currentVersion"`
		CoreVersion      string `json:"coreVersion"`
		UpdateInProgress bool   `json:"updateInProgress"`
	}
	if err := c.do(ctx, http.MethodGet, "/status", nil, &body); err != nil {
		return api.SupervisorStatus{}
	}
	return api.SupervisorStatus{
		Reachable:        true,
		Version:          body.CoreVersion,
		CurrentVersion:   body.CurrentVersion,
		UpdateInProgress: body.UpdateInProgress,
	}
}

// UpdateHistory returns the supervisor's update records (GET /updates, the "updates" array) as raw JSON.
// The in-flight journal in the same response is not returned.
func (c *Client) UpdateHistory(ctx context.Context) ([]json.RawMessage, error) {
	var body struct {
		Updates []json.RawMessage `json:"updates"`
	}
	if err := c.do(ctx, http.MethodGet, "/updates", nil, &body); err != nil {
		return nil, err
	}
	if body.Updates == nil {
		body.Updates = []json.RawMessage{}
	}
	return body.Updates, nil
}

// Update asks the supervisor to install version (POST /update). The supervisor runs the update to completion
// before it answers, so this call blocks for the whole update. The API layer runs it in a goroutine.
func (c *Client) Update(ctx context.Context, version string) error {
	payload, err := json.Marshal(map[string]string{"version": version})
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, "/update", payload, nil)
}

// Rollback asks the supervisor to switch back to the version before the current one (POST /rollback).
// The supervisor's rollback takes no target: it always restores its recorded previous version. updateID is
// therefore not sent; the API layer is responsible for checking that updateID is the update being rolled back.
func (c *Client) Rollback(ctx context.Context, updateID string) error {
	_ = updateID
	return c.do(ctx, http.MethodPost, "/rollback", nil, nil)
}

// do performs one authenticated request. A non-2xx answer becomes an error carrying the supervisor's message.
// When out is non-nil the 2xx body is decoded into it.
func (c *Client) do(ctx context.Context, method, path string, payload []byte, out any) error {
	var rdr io.Reader
	if payload != nil {
		rdr = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return fmt.Errorf("supervisor %s %s: %w", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("supervisor %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("supervisor %s %s: read body: %w", method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &StatusError{Code: resp.StatusCode, Message: errorMessage(raw, resp.StatusCode)}
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("supervisor %s %s: decode: %w", method, path, err)
		}
	}
	return nil
}

// StatusError is a non-2xx answer from the supervisor.
type StatusError struct {
	Code    int
	Message string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("supervisor answered %d: %s", e.Code, e.Message)
}

// errorMessage extracts {"error": "..."} from a failed answer, falling back to the status text.
func errorMessage(raw []byte, code int) string {
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &body); err == nil && strings.TrimSpace(body.Error) != "" {
		return body.Error
	}
	return strings.ToLower(http.StatusText(code))
}
