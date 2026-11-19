package supervisor

import (
	"fmt"
	"os"
	"path/filepath"
)

// DefaultCoreListen is the core's default listen address (docs/API_SPEC.md, §3 config).
const DefaultCoreListen = "127.0.0.1:7780"

// DefaultSupervisorAddr is the supervisor's loopback HTTP API address.
const DefaultSupervisorAddr = "127.0.0.1:7781"

// Home is the resolved $RHYMEHUB_HOME layout (docs/ARCHITECTURE.md §3).
type Home struct {
	Root string
}

// ResolveHome returns an absolute home directory. Precedence: explicit flag value,
// $RHYMEHUB_HOME, then ~/.rhymehub.
func ResolveHome(flagValue string) (Home, error) {
	root := flagValue
	if root == "" {
		root = os.Getenv("RHYMEHUB_HOME")
	}
	if root == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return Home{}, err
		}
		root = filepath.Join(userHome, ".rhymehub")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return Home{}, err
	}
	return Home{Root: abs}, nil
}

// Current is the `current` symlink (-> versions/<version>).
func (h Home) Current() string { return filepath.Join(h.Root, "current") }

// CurrentBinary is the core binary reached through the current symlink.
func (h Home) CurrentBinary() string { return filepath.Join(h.Root, "current", "bin", "rhymehub") }

// VersionsDir holds one directory per installed version.
func (h Home) VersionsDir() string { return filepath.Join(h.Root, "versions") }

// VersionDir is the install directory for a version.
func (h Home) VersionDir(version string) string { return filepath.Join(h.Root, "versions", version) }

// DBPath is the core SQLite database.
func (h Home) DBPath() string { return filepath.Join(h.Root, "rhymehub.db") }

// ConfigPath is config.json.
func (h Home) ConfigPath() string { return filepath.Join(h.Root, "config.json") }

// SecretsDir holds 0700 secrets (supervisor token, signing private key).
func (h Home) SecretsDir() string { return filepath.Join(h.Root, "secrets") }

// TokenPath is the supervisor bearer token.
func (h Home) TokenPath() string { return filepath.Join(h.Root, "secrets", "supervisor.token") }

// KeysDir holds trusted public keys.
func (h Home) KeysDir() string { return filepath.Join(h.Root, "keys") }

// PublicKeyPath is the trusted release public key used by the supervisor.
func (h Home) PublicKeyPath() string {
	return filepath.Join(h.Root, "keys", "release-signing.pub")
}

// PrivateKeyPath is the release signing private key.
func (h Home) PrivateKeyPath() string {
	return filepath.Join(h.Root, "secrets", "release-signing.key")
}

// BackupsDir holds pre-update DB backups.
func (h Home) BackupsDir() string { return filepath.Join(h.Root, "backups") }

// LogsDir holds core.log and supervisor logs.
func (h Home) LogsDir() string { return filepath.Join(h.Root, "logs") }

// DownloadsDir holds tarballs while an update is in flight.
func (h Home) DownloadsDir() string { return filepath.Join(h.Root, "downloads") }

// UpdatesLogPath is updates.jsonl.
func (h Home) UpdatesLogPath() string { return filepath.Join(h.Root, "updates.jsonl") }

// JournalPath is update-journal.json.
func (h Home) JournalPath() string { return filepath.Join(h.Root, "update-journal.json") }

// PidPath records the pid of the core process the supervisor started.
func (h Home) PidPath() string { return filepath.Join(h.Root, "core.pid") }

// StatePath is supervisor-state.json (previous version pointer).
func (h Home) StatePath() string { return filepath.Join(h.Root, "supervisor-state.json") }

// EnsureLayout creates the directories the supervisor needs.
func (h Home) EnsureLayout() error {
	dirs := []struct {
		path string
		perm os.FileMode
	}{
		{h.Root, 0o755},
		{h.VersionsDir(), 0o755},
		{h.SecretsDir(), 0o700},
		{h.KeysDir(), 0o755},
		{h.BackupsDir(), 0o755},
		{h.LogsDir(), 0o755},
		{h.DownloadsDir(), 0o755},
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d.path, d.perm); err != nil {
			return fmt.Errorf("create %s: %w", d.path, err)
		}
	}
	return os.Chmod(h.SecretsDir(), 0o700)
}
