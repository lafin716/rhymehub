package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/lafin716/rhymehub/internal/domain"
	"github.com/lafin716/rhymehub/internal/store"
)

// systemStatus mirrors GET /api/v1/system/status (API_SPEC). Unknown fields are ignored.
type systemStatus struct {
	Version       string `json:"version"`
	SchemaVersion int    `json:"schemaVersion"`
	UptimeSec     int64  `json:"uptimeSec"`
	Draining      bool   `json:"draining"`
	ActiveWorkers int    `json:"activeWorkers"`
	Supervisor    struct {
		Reachable        bool   `json:"reachable"`
		Version          string `json:"version"`
		CurrentVersion   string `json:"currentVersion"`
		UpdateInProgress bool   `json:"updateInProgress"`
	} `json:"supervisor"`
}

// runStatus prints a summary. With --server it asks the server (RHYMEHUB_TOKEN is sent when set). Otherwise it
// reads the local database: projects, active tasks and queue counts.
func runStatus(ctx context.Context, env Env, args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	serverFlag := fs.String("server", "", "query a remote RhymeHub server instead of the local database")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if *serverFlag != "" {
		return statusRemote(ctx, env, *serverFlag)
	}
	return statusLocal(ctx, env)
}

func statusRemote(ctx context.Context, env Env, server string) int {
	u, err := url.Parse(server)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		fmt.Fprintln(env.Stderr, "status: --server must be an http(s) URL")
		return ExitUsage
	}
	if u.User != nil {
		fmt.Fprintln(env.Stderr, "status: --server must not contain credentials")
		return ExitUsage
	}
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		fmt.Fprintln(env.Stderr, "warning: --server uses plain HTTP; the token will be sent unencrypted")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(server, "/")+"/api/v1/system/status", nil)
	if err != nil {
		fmt.Fprintln(env.Stderr, "status:", err)
		return ExitFailure
	}
	if token := os.Getenv("RHYMEHUB_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintln(env.Stderr, "status: request failed:", err)
		return ExitFailure
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		fmt.Fprintf(env.Stderr, "status: server returned %d: %s\n", resp.StatusCode, apiErrorMessage(raw))
		return ExitFailure
	}
	var s systemStatus
	if err := json.Unmarshal(raw, &s); err != nil {
		fmt.Fprintln(env.Stderr, "status: unexpected server response")
		return ExitFailure
	}
	fmt.Fprintf(env.Stdout, "server:       %s\n", u.Host)
	fmt.Fprintf(env.Stdout, "version:      %s\n", s.Version)
	fmt.Fprintf(env.Stdout, "schema:       %d\n", s.SchemaVersion)
	fmt.Fprintf(env.Stdout, "uptime:       %ds\n", s.UptimeSec)
	fmt.Fprintf(env.Stdout, "draining:     %t\n", s.Draining)
	fmt.Fprintf(env.Stdout, "workers:      %d active\n", s.ActiveWorkers)
	fmt.Fprintf(env.Stdout, "supervisor:   reachable=%t version=%s current=%s updating=%t\n",
		s.Supervisor.Reachable, s.Supervisor.Version, s.Supervisor.CurrentVersion, s.Supervisor.UpdateInProgress)
	return ExitOK
}

// activeTaskStates are the states that still need the worker or a human. Terminal states are excluded.
var activeTaskStates = []domain.TaskState{
	domain.StateCreated, domain.StateQueued, domain.StatePreparing, domain.StateRunning,
	domain.StateWaitingInput, domain.StateWaitingApproval, domain.StateValidating, domain.StateReviewReady,
	domain.StateApproved, domain.StateMerging, domain.StateReleasing, domain.StateRetrying,
}

func statusLocal(ctx context.Context, env Env) int {
	p, err := resolvePaths(env)
	if err != nil {
		fmt.Fprintln(env.Stderr, "status:", err)
		return ExitFailure
	}
	if _, err := os.Stat(p.DB()); err != nil {
		fmt.Fprintln(env.Stderr, "status: database not found; run rhymehub init first")
		return ExitFailure
	}
	st, err := store.Open(p.DB())
	if err != nil {
		fmt.Fprintln(env.Stderr, "status: open database:", err)
		return ExitFailure
	}
	defer st.Close()

	projects, err := st.ListProjects(ctx, false)
	if err != nil {
		fmt.Fprintln(env.Stderr, "status: list projects:", err)
		return ExitFailure
	}
	counts, err := st.CountByState(ctx)
	if err != nil {
		fmt.Fprintln(env.Stderr, "status: count tasks:", err)
		return ExitFailure
	}
	running, err := st.CountRunning(ctx)
	if err != nil {
		fmt.Fprintln(env.Stderr, "status: count running:", err)
		return ExitFailure
	}

	fmt.Fprintf(env.Stdout, "home:      %s\n", p.Root)
	fmt.Fprintf(env.Stdout, "projects:  %d\n", len(projects))
	for _, pr := range projects {
		fmt.Fprintf(env.Stdout, "  - %s (%s)\n", pr.Name, pr.ID)
	}
	active := 0
	for _, s := range activeTaskStates {
		active += counts[s]
	}
	fmt.Fprintf(env.Stdout, "active tasks: %d\n", active)
	fmt.Fprintf(env.Stdout, "queued:    %d\n", counts[domain.StateQueued])
	fmt.Fprintf(env.Stdout, "running:   %d\n", running)

	// Per-state breakdown of active tasks, sorted for stable output.
	var lines []string
	for _, s := range activeTaskStates {
		if n := counts[s]; n > 0 {
			lines = append(lines, fmt.Sprintf("  %-18s %d", s, n))
		}
	}
	sort.Strings(lines)
	for _, l := range lines {
		fmt.Fprintln(env.Stdout, l)
	}
	return ExitOK
}
