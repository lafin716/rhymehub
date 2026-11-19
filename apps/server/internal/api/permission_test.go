package api

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/lafin716/rhymehub/internal/domain"
)

// A Bash denial is granted as a narrow command-prefix rule, never as bare "Bash"; hard-denied and
// forbidden commands are refused and never stored.
func TestPermissionGrantsNarrowBashRule(t *testing.T) {
	e := newTaskEnv(t, newFakeReleaser())
	admin := e.session("root")
	e.makeProject("demo")
	id := e.createTask(admin, "demo", "fix the Mul gap")
	e.advanceTo(id, domain.StatePreparing, domain.StateRunning, domain.StateWaitingApproval)

	pending := []map[string]any{
		{"tool": "Bash", "input": map[string]string{"command": "go test ./..."}},
		{"tool": "Bash", "input": map[string]string{"command": "go test -run X ./..."}},
		{"tool": "Bash", "input": map[string]string{"command": "curl https://example.invalid"}},
		{"tool": "Bash", "input": map[string]string{"command": "sudo make install"}},
		{"tool": "Bash", "input": map[string]string{}},
		{"tool": "WebFetch", "input": map[string]string{"url": "https://example.invalid"}},
	}
	raw, _ := json.Marshal(pending)
	if _, err := e.srv.st.MutateTask(context.Background(), id, func(t *domain.Task) error {
		t.PendingApproval = raw
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	rec := e.do(http.MethodPost, "/api/v1/tasks/"+id+"/permission", map[string]bool{"allow": true},
		withCookie(admin), withCSRF())
	if rec.Code != http.StatusOK {
		t.Fatalf("permission: %d %s", rec.Code, rec.Body.String())
	}
	got, err := e.srv.st.GetTask(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != domain.StateQueued {
		t.Fatalf("state = %s, want QUEUED", got.State)
	}
	if !slices.Equal(got.AllowedTools, []string{"Bash(go test:*)"}) {
		t.Fatalf("allowedTools = %v, want [Bash(go test:*)]", got.AllowedTools)
	}
	if len(got.PendingApproval) != 0 {
		t.Fatalf("pending approval not cleared: %s", got.PendingApproval)
	}
}
