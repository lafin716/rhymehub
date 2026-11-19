package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHomeHonorsEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvHome, dir)
	got, err := Home()
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Fatalf("Home() = %q, want %q", got, dir)
	}
}

func TestPathsLayout(t *testing.T) {
	root := filepath.Join(t.TempDir(), "rh")
	p := NewPaths(root)
	if p.DB() != filepath.Join(root, "rhymehub.db") {
		t.Errorf("DB = %s", p.DB())
	}
	if p.SecretsDir() != filepath.Join(root, "secrets") {
		t.Errorf("SecretsDir = %s", p.SecretsDir())
	}
	if p.UpdateJournal() != filepath.Join(root, "update-journal.json") {
		t.Errorf("UpdateJournal = %s", p.UpdateJournal())
	}
}

func TestEnsureDirsCreatesPrivateLayout(t *testing.T) {
	p := NewPaths(filepath.Join(t.TempDir(), "home"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	// Re-running must succeed and keep secrets at 0700 even if loosened.
	if err := os.Chmod(p.SecretsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	for _, d := range p.Dirs() {
		fi, err := os.Stat(d)
		if err != nil {
			t.Fatalf("missing %s: %v", d, err)
		}
		if !fi.IsDir() {
			t.Fatalf("%s is not a directory", d)
		}
	}
	fi, err := os.Stat(p.SecretsDir())
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("secrets perm = %o, want 700", fi.Mode().Perm())
	}
}

func TestDefaults(t *testing.T) {
	cfg := Default()
	if cfg.Listen != "0.0.0.0:7780" {
		t.Errorf("Listen = %s", cfg.Listen)
	}
	if cfg.Scheduler.GlobalMaxRunning != 2 || cfg.Scheduler.DefaultPerAgent != 1 {
		t.Errorf("scheduler defaults = %+v", cfg.Scheduler)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMissingReturnsDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != Default().Listen {
		t.Fatalf("Listen = %s", cfg.Listen)
	}
}

func TestSaveLoadRoundTripAndMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := Default()
	cfg.Scheduler.PerAgent = map[string]int{"claude": 3}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("config perm = %o, want 600", fi.Mode().Perm())
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Scheduler.PerAgent["claude"] != 3 || got.Listen != cfg.Listen {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if e.Name() != "config.json" {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

func TestValidateRejectsHalfTLS(t *testing.T) {
	cfg := Default()
	cfg.TLSCert = "/tmp/cert.pem"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error when only tlsCert is set")
	}
	if err := Save(filepath.Join(t.TempDir(), "c.json"), cfg); err == nil {
		t.Fatal("Save must reject invalid config")
	}
}

func TestLoadRejectsBadJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected parse error")
	}
}
