// Package cli implements the rhymehub command-line subcommands. Run is separated from main so tests can
// drive every command with a temporary home directory.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lafin716/rhymehub/apps/server/internal/api"
	"github.com/lafin716/rhymehub/internal/config"
	"github.com/lafin716/rhymehub/internal/store"
)

// Exit codes. ExitMigrationFailed is part of the operator contract (migrate exits 3 on failure).
const (
	ExitOK              = 0
	ExitFailure         = 1
	ExitUsage           = 2
	ExitMigrationFailed = 3
)

// Env bundles what a command needs from the process.
type Env struct {
	Version string
	Stdout  io.Writer
	Stderr  io.Writer
	// Home overrides config.Home() when non-empty (tests use this instead of RHYMEHUB_HOME).
	Home string
}

// Run executes one subcommand and returns the process exit code.
func Run(ctx context.Context, env Env, args []string) int {
	if len(args) == 0 {
		usage(env.Stderr)
		return ExitUsage
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "version", "--version":
		fmt.Fprintf(env.Stdout, "rhymehub %s\n", env.Version)
		return ExitOK
	case "help", "-h", "--help":
		usage(env.Stdout)
		return ExitOK
	case "init":
		return runInit(ctx, env, rest)
	case "migrate":
		return runMigrate(ctx, env, rest)
	case "serve":
		return runServe(ctx, env, rest)
	case "worker", "register", "doctor", "status":
		fmt.Fprintf(env.Stderr, "rhymehub %s: not implemented in this build\n", cmd)
		return ExitUsage
	default:
		fmt.Fprintf(env.Stderr, "rhymehub: unknown command %q\n", cmd)
		usage(env.Stderr)
		return ExitUsage
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: rhymehub <command>")
	fmt.Fprintln(w, "commands: serve | worker | init | register | doctor | status | version | migrate")
}

func resolvePaths(env Env) (config.Paths, error) {
	root := env.Home
	if root == "" {
		h, err := config.Home()
		if err != nil {
			return config.Paths{}, err
		}
		root = h
	}
	return config.NewPaths(root), nil
}

// runInit creates the home layout, writes a default config.json when none exists, and migrates the database.
func runInit(ctx context.Context, env Env, args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	p, err := resolvePaths(env)
	if err != nil {
		fmt.Fprintln(env.Stderr, "init:", err)
		return ExitFailure
	}
	if err := p.EnsureDirs(); err != nil {
		fmt.Fprintln(env.Stderr, "init:", err)
		return ExitFailure
	}
	if _, err := os.Stat(p.ConfigFile()); errors.Is(err, os.ErrNotExist) {
		if err := config.Save(p.ConfigFile(), config.Default()); err != nil {
			fmt.Fprintln(env.Stderr, "init:", err)
			return ExitFailure
		}
	}
	if code := migrateDB(ctx, env, p); code != ExitOK {
		return code
	}
	fmt.Fprintf(env.Stdout, "initialized %s\n", p.Root)
	return ExitOK
}

func runMigrate(ctx context.Context, env Env, args []string) int {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	p, err := resolvePaths(env)
	if err != nil {
		fmt.Fprintln(env.Stderr, "migrate:", err)
		return ExitFailure
	}
	if err := p.EnsureDirs(); err != nil {
		fmt.Fprintln(env.Stderr, "migrate:", err)
		return ExitFailure
	}
	return migrateDB(ctx, env, p)
}

// migrateDB opens the database and applies pending migrations. Migration failures exit with
// ExitMigrationFailed so the supervisor can distinguish them from ordinary errors.
func migrateDB(ctx context.Context, env Env, p config.Paths) int {
	st, err := store.Open(p.DB())
	if err != nil {
		fmt.Fprintln(env.Stderr, "migrate: open database:", err)
		return ExitFailure
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		fmt.Fprintln(env.Stderr, "migrate:", err)
		if store.ErrMigrationFailed(err) {
			return ExitMigrationFailed
		}
		return ExitFailure
	}
	return ExitOK
}

func runServe(ctx context.Context, env Env, args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	listen := fs.String("listen", "", "override listen address (default from config.json)")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	p, err := resolvePaths(env)
	if err != nil {
		fmt.Fprintln(env.Stderr, "serve:", err)
		return ExitFailure
	}
	if err := p.EnsureDirs(); err != nil {
		fmt.Fprintln(env.Stderr, "serve:", err)
		return ExitFailure
	}
	cfg, err := config.Load(p.ConfigFile())
	if err != nil {
		fmt.Fprintln(env.Stderr, "serve:", err)
		return ExitFailure
	}
	if *listen != "" {
		cfg.Listen = *listen
	}
	if code := migrateDB(ctx, env, p); code != ExitOK {
		return code
	}
	st, err := store.Open(p.DB())
	if err != nil {
		fmt.Fprintln(env.Stderr, "serve: open database:", err)
		return ExitFailure
	}
	defer st.Close()

	logger := slog.New(slog.NewTextHandler(env.Stderr, nil))
	srv, err := api.New(api.Options{Store: st, Config: cfg, Version: env.Version, Logger: logger})
	if err != nil {
		fmt.Fprintln(env.Stderr, "serve:", err)
		return ExitFailure
	}
	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.ListenAndServe() }()
	logger.Info("rhymehub serving", "listen", cfg.Listen, "version", env.Version)

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(env.Stderr, "serve:", err)
			return ExitFailure
		}
		return ExitOK
	case <-sigCtx.Done():
		srv.SetDraining(true)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			fmt.Fprintln(env.Stderr, "serve: shutdown:", err)
			return ExitFailure
		}
		return ExitOK
	}
}
