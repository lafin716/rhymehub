package cli

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/lafin716/rhymehub/internal/config"
	"github.com/lafin716/rhymehub/internal/store"
	adapters "github.com/lafin716/rhymehub/packages/agent-adapters"
)

// Check severities. FAIL makes doctor exit 1; WARN is informational.
const (
	checkPass = "PASS"
	checkWarn = "WARN"
	checkFail = "FAIL"
)

type doctorCheck struct {
	status string
	name   string
	msg    string
}

// runDoctor reports the local installation. It never reads agent credential stores; agent checks only run the
// CLI's own version probe through the adapter.
func runDoctor(ctx context.Context, env Env, args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	p, err := resolvePaths(env)
	if err != nil {
		fmt.Fprintln(env.Stderr, "doctor:", err)
		return ExitFailure
	}

	checks := []doctorCheck{
		checkTool(ctx, "git", true),
		checkTool(ctx, "go", false),
		checkTool(ctx, "node", false),
		checkAgent(ctx, "claude"),
		checkAgent(ctx, "codex"),
		checkHomePerms(p),
	}
	cfg, cfgErr := config.Load(p.ConfigFile())
	if cfgErr != nil {
		checks = append(checks, doctorCheck{checkFail, "config", cfgErr.Error()})
	} else {
		checks = append(checks, checkSchema(ctx, p), checkSupervisor(cfg), checkTLS(cfg))
	}

	failed := false
	for _, c := range checks {
		fmt.Fprintf(env.Stdout, "%s %s: %s\n", c.status, c.name, c.msg)
		if c.status == checkFail {
			failed = true
		}
	}
	if failed {
		return ExitFailure
	}
	return ExitOK
}

// checkTool looks for a binary on PATH and records its version line. Missing git is a FAIL because the worker
// cannot run without it; missing go or node is a WARN because they are only needed by some projects.
func checkTool(ctx context.Context, bin string, required bool) doctorCheck {
	path, err := exec.LookPath(bin)
	if err != nil {
		if required {
			return doctorCheck{checkFail, bin, "not found on PATH"}
		}
		return doctorCheck{checkWarn, bin, "not found on PATH"}
	}
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(runCtx, path, "--version").CombinedOutput()
	version := strings.TrimSpace(string(out))
	if err != nil || version == "" {
		return doctorCheck{checkWarn, bin, fmt.Sprintf("found at %s but version probe failed", path)}
	}
	if i := strings.IndexByte(version, '\n'); i >= 0 {
		version = version[:i]
	}
	return doctorCheck{checkPass, bin, version}
}

// checkAgent asks the adapter whether the agent CLI runs. Absence is a WARN: RhymeHub runs without every agent.
func checkAgent(ctx context.Context, name string) doctorCheck {
	a, err := adapters.Get(name)
	if err != nil {
		return doctorCheck{checkWarn, "agent " + name, err.Error()}
	}
	res := a.Detect(ctx)
	if !res.Available {
		msg := "not available"
		if res.Error != "" {
			msg += ": " + res.Error
		}
		return doctorCheck{checkWarn, "agent " + name, msg}
	}
	return doctorCheck{checkPass, "agent " + name, fmt.Sprintf("version %s (%s)", res.Version, res.Path)}
}

// checkHomePerms verifies the secrets directory is private (0700). Only the mode is inspected.
func checkHomePerms(p config.Paths) doctorCheck {
	info, err := os.Stat(p.Root)
	if err != nil {
		return doctorCheck{checkWarn, "home", fmt.Sprintf("%s not found; run rhymehub init", p.Root)}
	}
	if !info.IsDir() {
		return doctorCheck{checkFail, "home", p.Root + " is not a directory"}
	}
	sec, err := os.Stat(p.SecretsDir())
	if err != nil {
		return doctorCheck{checkWarn, "secrets dir", "missing; run rhymehub init"}
	}
	if mode := sec.Mode().Perm(); mode != 0o700 {
		return doctorCheck{checkFail, "secrets dir", fmt.Sprintf("mode %o, want 700", mode)}
	}
	return doctorCheck{checkPass, "secrets dir", "mode 700"}
}

// checkSchema compares the database schema version with the newest embedded migration.
func checkSchema(ctx context.Context, p config.Paths) doctorCheck {
	if _, err := os.Stat(p.DB()); err != nil {
		return doctorCheck{checkWarn, "database", "not initialized; run rhymehub init"}
	}
	migs, err := store.Migrations()
	if err != nil {
		return doctorCheck{checkFail, "database", err.Error()}
	}
	latest := 0
	if len(migs) > 0 {
		latest = migs[len(migs)-1].Version
	}
	st, err := store.Open(p.DB())
	if err != nil {
		return doctorCheck{checkFail, "database", "open: " + err.Error()}
	}
	defer st.Close()
	version, err := st.SchemaVersion(ctx)
	if err != nil {
		return doctorCheck{checkFail, "database", "schema version: " + err.Error()}
	}
	if version < latest {
		return doctorCheck{checkWarn, "database", fmt.Sprintf("schema version %d, latest %d; run rhymehub migrate", version, latest)}
	}
	return doctorCheck{checkPass, "database", fmt.Sprintf("schema version %d", version)}
}

// checkSupervisor dials the supervisor control endpoint. An unreachable supervisor is a WARN; the server can
// still run without updates.
func checkSupervisor(cfg config.Config) doctorCheck {
	if cfg.SupervisorAddr == "" {
		return doctorCheck{checkWarn, "supervisor", "supervisorAddr not configured"}
	}
	conn, err := net.DialTimeout("tcp", cfg.SupervisorAddr, time.Second)
	if err != nil {
		return doctorCheck{checkWarn, "supervisor", fmt.Sprintf("%s unreachable", cfg.SupervisorAddr)}
	}
	conn.Close()
	return doctorCheck{checkPass, "supervisor", fmt.Sprintf("%s reachable", cfg.SupervisorAddr)}
}

// checkTLS warns when the server listens on a non-loopback address without TLS, which sends tokens in plain
// text. A configured certificate that does not exist is a FAIL.
func checkTLS(cfg config.Config) doctorCheck {
	if cfg.TLSCert != "" {
		if !fileExists(cfg.TLSCert) || !fileExists(cfg.TLSKey) {
			return doctorCheck{checkFail, "tls", "tlsCert or tlsKey file is missing"}
		}
		return doctorCheck{checkPass, "tls", "certificate configured"}
	}
	host, _, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		host = cfg.Listen
	}
	if isLoopbackHost(host) {
		return doctorCheck{checkPass, "tls", "listen address is loopback; TLS not required"}
	}
	return doctorCheck{checkWarn, "tls", "plain HTTP on " + cfg.Listen + "; configure tlsCert and tlsKey or keep traffic on a trusted network"}
}
