package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lafin716/rhymehub/internal/config"
)

func TestCheckHomePermsFailsOnOpenSecrets(t *testing.T) {
	env, _, _ := initTestHome(t)
	p := config.NewPaths(env.Home)
	if err := os.Chmod(p.SecretsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	c := checkHomePerms(p)
	if c.status != checkFail {
		t.Fatalf("status %s (%s), want FAIL", c.status, c.msg)
	}
}

func TestCheckHomePermsPassesOnPrivateSecrets(t *testing.T) {
	env, _, _ := initTestHome(t)
	p := config.NewPaths(env.Home)
	if err := os.Chmod(p.SecretsDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if c := checkHomePerms(p); c.status != checkPass {
		t.Fatalf("status %s (%s), want PASS", c.status, c.msg)
	}
}

func TestCheckHomePermsWarnsWhenHomeMissing(t *testing.T) {
	p := config.NewPaths(filepath.Join(t.TempDir(), "absent"))
	if c := checkHomePerms(p); c.status != checkWarn {
		t.Fatalf("status %s (%s), want WARN", c.status, c.msg)
	}
}

func TestCheckTLSWarnsOnPlainNonLoopback(t *testing.T) {
	c := checkTLS(config.Config{Listen: "0.0.0.0:7780"})
	if c.status != checkWarn {
		t.Fatalf("status %s (%s), want WARN", c.status, c.msg)
	}
}

func TestCheckTLSPassesOnLoopback(t *testing.T) {
	c := checkTLS(config.Config{Listen: "127.0.0.1:7780"})
	if c.status != checkPass {
		t.Fatalf("status %s (%s), want PASS", c.status, c.msg)
	}
}

func TestCheckTLSFailsOnMissingCertFile(t *testing.T) {
	c := checkTLS(config.Config{Listen: "0.0.0.0:7780", TLSCert: "/nonexistent/cert.pem", TLSKey: "/nonexistent/key.pem"})
	if c.status != checkFail {
		t.Fatalf("status %s (%s), want FAIL", c.status, c.msg)
	}
}

func TestCheckSupervisorUnconfiguredWarns(t *testing.T) {
	if c := checkSupervisor(config.Config{}); c.status != checkWarn {
		t.Fatalf("status %s, want WARN", c.status)
	}
}

func TestCheckToolMissingBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if c := checkTool(t.Context(), "git", true); c.status != checkFail {
		t.Fatalf("required missing tool status %s, want FAIL", c.status)
	}
	if c := checkTool(t.Context(), "go", false); c.status != checkWarn {
		t.Fatalf("optional missing tool status %s, want WARN", c.status)
	}
}
