// Package config resolves the rhymehub home directory, its on-disk layout, and the config.json file.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// EnvHome overrides the default home directory.
const EnvHome = "RHYMEHUB_HOME"

// Home returns $RHYMEHUB_HOME when set, otherwise ~/.rhymehub.
func Home() (string, error) {
	if v := os.Getenv(EnvHome); v != "" {
		return filepath.Abs(v)
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: resolve home: %w", err)
	}
	return filepath.Join(h, ".rhymehub"), nil
}

// Paths is the on-disk layout rooted at one home directory. Every entry is derived from Root.
type Paths struct {
	Root string
}

// NewPaths returns the layout for an explicit root. Use Home() to resolve the default root.
func NewPaths(root string) Paths { return Paths{Root: root} }

func (p Paths) join(elems ...string) string {
	return filepath.Join(append([]string{p.Root}, elems...)...)
}

// ConfigFile is config.json.
func (p Paths) ConfigFile() string { return p.join("config.json") }

// DB is the SQLite database rhymehub.db.
func (p Paths) DB() string { return p.join("rhymehub.db") }

// SecretsDir holds secrets (mode 0700). Agents must never be granted access to it.
func (p Paths) SecretsDir() string { return p.join("secrets") }

// KeysDir holds signing and key material.
func (p Paths) KeysDir() string { return p.join("keys") }

// VersionsDir holds installed versions of the binary.
func (p Paths) VersionsDir() string { return p.join("versions") }

// CurrentLink is the symlink that points at the active version.
func (p Paths) CurrentLink() string { return p.join("current") }

// ReleasesDir holds release artifacts.
func (p Paths) ReleasesDir() string { return p.join("releases") }

// BackupsDir holds database and data backups.
func (p Paths) BackupsDir() string { return p.join("backups") }

// WorktreesDir holds per-task git worktrees.
func (p Paths) WorktreesDir() string { return p.join("worktrees") }

// AttachmentsDir holds uploaded request attachments (outside worktrees).
func (p Paths) AttachmentsDir() string { return p.join("attachments") }

// LogsDir holds service and task logs.
func (p Paths) LogsDir() string { return p.join("logs") }

// UpdatesLog is updates.jsonl, the append-only update history.
func (p Paths) UpdatesLog() string { return p.join("updates.jsonl") }

// UpdateJournal is update-journal.json, the in-progress update state for crash recovery.
func (p Paths) UpdateJournal() string { return p.join("update-journal.json") }

// Dirs lists every directory in the layout. Secrets is the only one that gets mode 0700 enforced on
// an existing directory; all of them are created 0700.
func (p Paths) Dirs() []string {
	return []string{
		p.Root, p.SecretsDir(), p.KeysDir(), p.VersionsDir(), p.ReleasesDir(),
		p.BackupsDir(), p.WorktreesDir(), p.AttachmentsDir(), p.LogsDir(),
	}
}

// EnsureDirs creates the layout directories with mode 0700 and tightens secrets/ to 0700 even if it existed.
func (p Paths) EnsureDirs() error {
	for _, d := range p.Dirs() {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("config: create %s: %w", d, err)
		}
	}
	if err := os.Chmod(p.SecretsDir(), 0o700); err != nil {
		return fmt.Errorf("config: chmod secrets: %w", err)
	}
	return nil
}

// SchedulerConfig holds concurrency limits used by ClaimNextQueued.
type SchedulerConfig struct {
	// GlobalMaxRunning caps PREPARING+RUNNING+VALIDATING tasks across all projects. <= 0 means unlimited.
	GlobalMaxRunning int `json:"globalMaxRunning"`
	// DefaultPerAgent caps running tasks per agent name when no PerAgent entry exists. <= 0 means unlimited.
	DefaultPerAgent int `json:"defaultPerAgent"`
	// PerAgent overrides DefaultPerAgent for specific agent names.
	PerAgent map[string]int `json:"perAgent"`
}

// Config is config.json. Field names are the JSON keys.
type Config struct {
	// Listen is the HTTP bind address. Dev servers bind 0.0.0.0 so Tailscale MagicDNS can reach them.
	Listen string `json:"listen"`
	// InsecureAllowedCIDRs are client networks allowed without TLS (Tailscale CGNAT, Tailscale IPv6, loopback).
	InsecureAllowedCIDRs []string `json:"insecureAllowedCIDRs"`
	// TLSCert and TLSKey are paths to a certificate and key. Both empty means plain HTTP.
	TLSCert string `json:"tlsCert"`
	TLSKey  string `json:"tlsKey"`
	// SupervisorAddr is the loopback address of the supervisor control endpoint.
	SupervisorAddr string `json:"supervisorAddr"`
	// Scheduler holds the concurrency limits.
	Scheduler SchedulerConfig `json:"scheduler"`
}

// Default returns the built-in configuration.
func Default() Config {
	return Config{
		Listen: "0.0.0.0:7780",
		InsecureAllowedCIDRs: []string{
			"100.64.0.0/10",       // Tailscale IPv4 (CGNAT)
			"fd7a:115c:a1e0::/48", // Tailscale IPv6
			"127.0.0.0/8",         // IPv4 loopback
			"::1/128",             // IPv6 loopback
		},
		SupervisorAddr: "127.0.0.1:7781",
		Scheduler: SchedulerConfig{
			GlobalMaxRunning: 2,
			DefaultPerAgent:  1,
			PerAgent:         map[string]int{},
		},
	}
}

// Validate checks required fields.
func (c Config) Validate() error {
	if c.Listen == "" {
		return errors.New("config: listen address is empty")
	}
	if c.SupervisorAddr == "" {
		return errors.New("config: supervisorAddr is empty")
	}
	if (c.TLSCert == "") != (c.TLSKey == "") {
		return errors.New("config: tlsCert and tlsKey must be set together")
	}
	return nil
}

// Load reads config.json. A missing file returns Default() with no error. Missing fields keep their defaults.
func Load(path string) (Config, error) {
	cfg := Default()
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("config: read %s: %w", path, err)
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return Config{}, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Save writes config.json atomically (temp file, fsync, rename) with mode 0600.
func Save(path string, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("config: encode: %w", err)
	}
	b = append(b, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("config: create dir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.json")
	if err != nil {
		return fmt.Errorf("config: temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return fmt.Errorf("config: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("config: sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("config: close temp: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("config: chmod temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("config: rename: %w", err)
	}
	return nil
}
