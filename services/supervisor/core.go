package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// coreProc is one running core process. Either cmd is set (we spawned it) or the process was
// adopted from the pidfile after a supervisor restart, in which case done is polled.
type coreProc struct {
	cmd     *exec.Cmd
	pid     int
	started time.Time
	done    chan struct{}
}

// exited reports whether the core process has terminated.
func (p *coreProc) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// spawnCore starts `<binary> serve` in its own process group. stdout and stderr go to core.log.
func spawnCore(h Home, binary string) (*coreProc, error) {
	if err := os.MkdirAll(h.LogsDir(), 0o755); err != nil {
		return nil, err
	}
	logf, err := os.OpenFile(filepath.Join(h.LogsDir(), "core.log"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(binary, "serve")
	cmd.Env = scrubbedEnv(h)
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_ = logf.Close()
		return nil, fmt.Errorf("start core %s: %w", binary, err)
	}
	p := &coreProc{cmd: cmd, pid: cmd.Process.Pid, started: time.Now(), done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		_ = logf.Close()
		close(p.done)
	}()
	if err := os.WriteFile(h.PidPath(), []byte(strconv.Itoa(p.pid)+"\n"), 0o600); err != nil {
		_ = syscall.Kill(-p.pid, syscall.SIGKILL)
		return nil, err
	}
	return p, nil
}

// adoptCore wraps a process started by an earlier supervisor instance. Its exit is detected by
// polling, because it is not our child.
func adoptCore(pid int) *coreProc {
	p := &coreProc{pid: pid, started: time.Now(), done: make(chan struct{})}
	go func() {
		for syscall.Kill(pid, 0) == nil {
			time.Sleep(200 * time.Millisecond)
		}
		close(p.done)
	}()
	return p
}

// stopCore sends SIGTERM to the core's process group, escalating to SIGKILL after timeout. The
// pidfile is removed once the process is gone.
func stopCore(h Home, p *coreProc, timeout time.Duration) error {
	if p == nil {
		return nil
	}
	if !p.exited() {
		_ = syscall.Kill(-p.pid, syscall.SIGTERM)
		if !waitDone(p, timeout) {
			_ = syscall.Kill(-p.pid, syscall.SIGKILL)
			if !waitDone(p, 5*time.Second) {
				return fmt.Errorf("core pid %d did not exit after SIGKILL", p.pid)
			}
		}
	}
	if err := os.Remove(h.PidPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// waitDone waits up to timeout for the process to exit.
func waitDone(p *coreProc, timeout time.Duration) bool {
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-p.done:
		return true
	case <-t.C:
		return false
	}
}

// killStaleCore stops a core left behind by an earlier supervisor run, identified by the
// pidfile. The process command line must mention this home, so a recycled pid is not touched.
func killStaleCore(h Home) error {
	data, err := os.ReadFile(h.PidPath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 1 {
		_ = os.Remove(h.PidPath())
		return nil
	}
	out, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err == nil && strings.Contains(string(out), h.Root) {
		p := adoptCore(pid)
		_ = syscall.Kill(-pid, syscall.SIGTERM)
		if !waitDone(p, 15*time.Second) {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			waitDone(p, 5*time.Second)
		}
	}
	return os.Remove(h.PidPath())
}

// healthReport is the subset of GET /healthz the supervisor uses.
type healthReport struct {
	Status        string `json:"status"`
	Version       string `json:"version"`
	SchemaVersion int    `json:"schemaVersion"`
	Draining      bool   `json:"draining"`
}

// queryHealth calls GET /healthz on the core.
func queryHealth(ctx context.Context, client *http.Client, addr string) (healthReport, error) {
	var hr healthReport
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/healthz", nil)
	if err != nil {
		return hr, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return hr, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return hr, fmt.Errorf("healthz: status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&hr); err != nil {
		return hr, fmt.Errorf("healthz: %w", err)
	}
	return hr, nil
}

// waitHealthy polls the core until it reports status ok with the expected version. It fails
// early if the process exits, and after timeout.
func waitHealthy(ctx context.Context, client *http.Client, addr string, p *coreProc, expected string, timeout, interval time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last error
	for {
		if p != nil && p.exited() {
			return fmt.Errorf("core exited before becoming healthy")
		}
		hr, err := queryHealth(ctx, client, addr)
		switch {
		case err != nil:
			last = err
		case hr.Status != "ok":
			last = fmt.Errorf("healthz status %q", hr.Status)
		case hr.Version != expected:
			last = fmt.Errorf("healthz version %q, want %s", hr.Version, expected)
		default:
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("healthcheck timed out after %s: %w", timeout, last)
		}
		if err := sleepCtx(ctx, interval); err != nil {
			return err
		}
	}
}

// drainCore asks the core to stop starting new tasks. Best effort; the caller decides whether a
// failure matters.
func drainCore(ctx context.Context, client *http.Client, addr, token string) error {
	body, _ := json.Marshal(map[string]bool{"drain": true})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/api/v1/system/drain", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("drain: status %d", resp.StatusCode)
	}
	return nil
}
