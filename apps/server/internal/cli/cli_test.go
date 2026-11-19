package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lafin716/rhymehub/internal/config"
)

func testEnv(t *testing.T) (Env, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var out, errb bytes.Buffer
	home := filepath.Join(t.TempDir(), "home")
	return Env{Version: "9.9.9-test", Stdout: &out, Stderr: &errb, Home: home}, &out, &errb
}

func TestVersion(t *testing.T) {
	env, out, _ := testEnv(t)
	if code := Run(context.Background(), env, []string{"version"}); code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out.String(), "9.9.9-test") {
		t.Fatalf("version output %q", out.String())
	}
}

func TestInitCreatesLayoutAndConfig(t *testing.T) {
	env, _, errb := testEnv(t)
	if code := Run(context.Background(), env, []string{"init"}); code != ExitOK {
		t.Fatalf("init exit %d: %s", code, errb.String())
	}
	p := config.NewPaths(env.Home)
	for _, f := range []string{p.ConfigFile(), p.DB()} {
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("missing %s: %v", f, err)
		}
	}
	// A second init must keep an existing config and still succeed.
	if code := Run(context.Background(), env, []string{"init"}); code != ExitOK {
		t.Fatalf("second init exit %d", code)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	env, _, errb := testEnv(t)
	for i := 0; i < 2; i++ {
		if code := Run(context.Background(), env, []string{"migrate"}); code != ExitOK {
			t.Fatalf("migrate run %d exit %d: %s", i, code, errb.String())
		}
	}
}

func TestUsageAndUnknownCommands(t *testing.T) {
	env, _, errb := testEnv(t)
	if code := Run(context.Background(), env, nil); code != ExitUsage {
		t.Fatalf("no args exit %d", code)
	}
	if code := Run(context.Background(), env, []string{"bogus"}); code != ExitUsage {
		t.Fatalf("unknown command exit %d", code)
	}
	// worker/register/doctor/status are dispatched to their implementations.
	errb.Reset()
	_ = Run(context.Background(), env, []string{"doctor"})
	if strings.Contains(errb.String(), "not implemented") {
		t.Fatalf("doctor not dispatched: stderr %q", errb.String())
	}
}
