package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"time"
)

// CoreConfig is the subset of config.json the supervisor needs. Unknown keys are ignored.
type CoreConfig struct {
	Listen     string `json:"listen"`
	UpdateFeed string `json:"updateFeed"`
}

// LoadCoreConfig reads $RHYMEHUB_HOME/config.json, falling back to defaults for missing keys
// (and for a missing file).
func LoadCoreConfig(h Home) (CoreConfig, error) {
	cfg := CoreConfig{
		Listen:     DefaultCoreListen,
		UpdateFeed: "file://" + filepath.Join(h.Root, "releases", "manifest.json"),
	}
	data, err := os.ReadFile(h.ConfigPath())
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", h.ConfigPath(), err)
	}
	if cfg.Listen == "" {
		cfg.Listen = DefaultCoreListen
	}
	return cfg, nil
}

// coreHealthAddr maps the core listen address to one the supervisor can dial. Wildcard binds
// are reached through loopback.
func coreHealthAddr(listen string) (string, error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("listen address %q: %w", listen, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port), nil
}

// scrubbedEnv builds the environment for child processes. Only what the core needs is passed,
// so supervisor secrets (tokens, keys) never leak through inherited variables.
func scrubbedEnv(h Home) []string {
	env := []string{"RHYMEHUB_HOME=" + h.Root}
	for _, k := range []string{"PATH", "HOME", "TMPDIR", "LANG", "TZ"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// sleepCtx sleeps for d or until ctx is done.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
