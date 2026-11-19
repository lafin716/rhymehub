// Package supervisor owns the rhymehub core process: it starts and restarts the core, applies
// signed updates with a journaled state machine, rolls back on failure, and exposes a loopback
// HTTP API protected by a bearer token.
package supervisor

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/lafin716/rhymehub/internal/signing"
)

// ErrBusy is returned when an update or rollback is requested while another one is running.
var ErrBusy = errors.New("supervisor: an update or rollback is already in progress")

// Options configures a Supervisor. Zero values select production defaults.
type Options struct {
	Home           Home
	FeedURL        string            // overrides config.updateFeed when set
	TrustedKey     ed25519.PublicKey // defaults to keys/release-signing.pub when present
	AllowUnsigned  bool              // dev only: accept artifacts without a signature
	SupervisorAddr string            // loopback API address, default 127.0.0.1:7781
	GOOS, GOARCH   string            // artifact platform, default runtime values
	HTTPClient     *http.Client      // feed and artifact downloads

	DrainTimeout     time.Duration // default 60s
	StopTimeout      time.Duration // SIGTERM grace before SIGKILL, default 20s
	HealthTimeout    time.Duration // default 45s
	HealthInterval   time.Duration // default 500ms
	BackoffMin       time.Duration // restart backoff floor, default 1s
	BackoffMax       time.Duration // restart backoff ceiling, default 30s
	MaxArtifactBytes int64         // default 512 MiB

	// PhaseHook runs after each update phase is journaled. A non-nil error aborts the update at
	// that point without recording an outcome, which simulates a crash for tests.
	PhaseHook func(phase string) error
	Logf      func(format string, args ...any)
}

// Supervisor is the long-running owner of the core process.
type Supervisor struct {
	opts       Options
	cfg        CoreConfig
	token      string
	pubKey     ed25519.PublicKey
	feedClient *http.Client
	probe      *http.Client

	mu      sync.Mutex
	core    *coreProc
	busy    bool
	running bool
	rootCtx context.Context
	// halted is non-empty while no core may run. It mirrors supervisorState.HaltedReason on disk.
	halted string
}

// errHalted is returned when a core start is refused because the supervisor is halted.
var errHalted = errors.New("supervisor is halted")

// New prepares the home layout, loads config and the trusted key, and ensures the API token.
func New(opts Options) (*Supervisor, error) {
	h := opts.Home
	if h.Root == "" {
		return nil, errors.New("supervisor: home is required")
	}
	if err := h.EnsureLayout(); err != nil {
		return nil, err
	}
	if opts.SupervisorAddr == "" {
		opts.SupervisorAddr = DefaultSupervisorAddr
	}
	if opts.GOOS == "" {
		opts.GOOS = runtime.GOOS
	}
	if opts.GOARCH == "" {
		opts.GOARCH = runtime.GOARCH
	}
	if opts.DrainTimeout == 0 {
		opts.DrainTimeout = 60 * time.Second
	}
	if opts.StopTimeout == 0 {
		opts.StopTimeout = 20 * time.Second
	}
	if opts.HealthTimeout == 0 {
		opts.HealthTimeout = 45 * time.Second
	}
	if opts.HealthInterval == 0 {
		opts.HealthInterval = 500 * time.Millisecond
	}
	if opts.BackoffMin == 0 {
		opts.BackoffMin = time.Second
	}
	if opts.BackoffMax == 0 {
		opts.BackoffMax = 30 * time.Second
	}
	if opts.MaxArtifactBytes == 0 {
		opts.MaxArtifactBytes = 512 << 20
	}
	if opts.Logf == nil {
		opts.Logf = log.Printf
	}
	cfg, err := LoadCoreConfig(h)
	if err != nil {
		return nil, err
	}
	if opts.FeedURL != "" {
		cfg.UpdateFeed = opts.FeedURL
	}
	if _, err := coreHealthAddr(cfg.Listen); err != nil {
		return nil, err
	}
	token, err := ensureToken(h)
	if err != nil {
		return nil, fmt.Errorf("supervisor token: %w", err)
	}
	pub := opts.TrustedKey
	if pub == nil {
		if _, statErr := os.Stat(h.PublicKeyPath()); statErr == nil {
			pub, err = signing.LoadPublicKey(h.PublicKeyPath())
			if err != nil {
				return nil, fmt.Errorf("trusted key: %w", err)
			}
		}
	}
	feed := opts.HTTPClient
	if feed == nil {
		feed = &http.Client{Timeout: 10 * time.Minute}
	}
	return &Supervisor{
		opts:       opts,
		cfg:        cfg,
		token:      token,
		pubKey:     pub,
		feedClient: feed,
		probe:      &http.Client{Timeout: 2 * time.Second},
		rootCtx:    context.Background(),
		halted:     readState(h).HaltedReason,
	}, nil
}

// Token returns the API bearer token (also stored in secrets/supervisor.token).
func (s *Supervisor) Token() string { return s.token }

// Run serves the loopback API, recovers any interrupted update, keeps the core running, and
// shuts everything down when ctx is cancelled.
func (s *Supervisor) Run(ctx context.Context) error {
	host, _, err := net.SplitHostPort(s.opts.SupervisorAddr)
	if err != nil {
		return fmt.Errorf("supervisor address: %w", err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("supervisor API must bind a loopback IP, got %q", host)
	}
	ln, err := net.Listen("tcp", s.opts.SupervisorAddr)
	if err != nil {
		return err
	}
	if err := killStaleCore(s.opts.Home); err != nil {
		_ = ln.Close()
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.mu.Lock()
	s.running = true
	s.rootCtx = ctx
	s.mu.Unlock()

	if err := s.Recover(ctx); err != nil {
		s.opts.Logf("recover: %v", err)
	}
	if err := s.EnsureCore(ctx); err != nil {
		s.opts.Logf("start core: %v", err)
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	go s.runLoop(ctx)

	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-serveErr:
	}
	shutdownCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	_ = srv.Shutdown(shutdownCtx)
	if err := s.Shutdown(); err != nil && runErr == nil {
		runErr = err
	}
	if errors.Is(runErr, http.ErrServerClosed) {
		runErr = nil
	}
	return runErr
}

// Shutdown stops supervising and stops the core.
func (s *Supervisor) Shutdown() error {
	s.mu.Lock()
	s.running = false
	p := s.core
	s.core = nil
	s.mu.Unlock()
	return stopCore(s.opts.Home, p, s.opts.StopTimeout)
}

// EnsureCore starts the current version's core unless one is already running. It refuses to start
// anything while the supervisor is halted.
func (s *Supervisor) EnsureCore(ctx context.Context) error {
	s.mu.Lock()
	halted := s.halted
	alive := s.core != nil && !s.core.exited()
	s.mu.Unlock()
	if halted != "" {
		return fmt.Errorf("%w: %s", errHalted, halted)
	}
	if alive {
		return nil
	}
	p, err := s.launchCurrent(ctx)
	if err != nil {
		return err
	}
	s.setCore(p)
	return nil
}

// launchCurrent starts the core from the current symlink and waits until it is healthy at the
// current version. A core that never becomes healthy is stopped again.
func (s *Supervisor) launchCurrent(ctx context.Context) (*coreProc, error) {
	h := s.opts.Home
	p, err := spawnCore(h, h.CurrentBinary())
	if err != nil {
		return nil, err
	}
	addr, err := coreHealthAddr(s.cfg.Listen)
	if err == nil {
		err = waitHealthy(ctx, s.probe, addr, p, currentVersion(h), s.opts.HealthTimeout, s.opts.HealthInterval)
	}
	if err != nil {
		_ = stopCore(h, p, s.opts.StopTimeout)
		return nil, err
	}
	return p, nil
}

func (s *Supervisor) setCore(p *coreProc) {
	s.mu.Lock()
	s.core = p
	s.mu.Unlock()
}

// halt stops the supervisor from starting any core and persists the reason, so the halt survives a
// restart. The in-memory flag is set first so runLoop cannot relaunch in the meantime.
func (s *Supervisor) halt(reason string) error {
	s.mu.Lock()
	s.halted = reason
	s.mu.Unlock()
	st := readState(s.opts.Home)
	st.HaltedReason = reason
	return writeState(s.opts.Home, st)
}

// saveState persists st and then updates the in-memory halt to match it. A state without a
// HaltedReason clears the halt.
func (s *Supervisor) saveState(st supervisorState) error {
	if err := writeState(s.opts.Home, st); err != nil {
		return err
	}
	s.mu.Lock()
	s.halted = st.HaltedReason
	s.mu.Unlock()
	return nil
}

// takeCore detaches the tracked core so the caller can stop it.
func (s *Supervisor) takeCore() *coreProc {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.core
	s.core = nil
	return p
}

func (s *Supervisor) currentCore() *coreProc {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.core
}

func (s *Supervisor) baseCtx() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rootCtx
}

// acquire takes the single update/rollback slot. It returns false while another operation runs.
func (s *Supervisor) acquire() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy {
		return false
	}
	s.busy = true
	return true
}

func (s *Supervisor) release() {
	s.mu.Lock()
	s.busy = false
	s.mu.Unlock()
}

// runLoop restarts the core when it exits on its own, with exponential backoff. The backoff
// resets once a core has stayed up for 30 seconds.
func (s *Supervisor) runLoop(ctx context.Context) {
	backoff := s.opts.BackoffMin
	var notBefore time.Time
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		s.mu.Lock()
		if !s.running || s.halted != "" || s.busy || (s.core != nil && !s.core.exited()) {
			s.mu.Unlock()
			continue
		}
		now := time.Now()
		if s.core != nil {
			if now.Sub(s.core.started) >= 30*time.Second {
				backoff = s.opts.BackoffMin
			}
			s.opts.Logf("core pid %d exited; restarting in %s", s.core.pid, backoff)
			notBefore = now.Add(backoff)
			backoff = min(2*backoff, s.opts.BackoffMax)
			s.core = nil
		}
		if now.Before(notBefore) {
			s.mu.Unlock()
			continue
		}
		s.busy = true
		s.mu.Unlock()

		p, err := s.launchCurrent(ctx)

		s.mu.Lock()
		s.busy = false
		if err == nil {
			s.core = p
		}
		s.mu.Unlock()
		if err != nil {
			s.opts.Logf("restart core: %v", err)
			notBefore = time.Now().Add(backoff)
			backoff = min(2*backoff, s.opts.BackoffMax)
		}
	}
}

// StatusReport is the body of GET /status.
type StatusReport struct {
	CurrentVersion   string `json:"currentVersion"`
	Previous         string `json:"previous,omitempty"`
	CoreRunning      bool   `json:"coreRunning"`
	CorePid          int    `json:"corePid,omitempty"`
	CoreHealthy      bool   `json:"coreHealthy"`
	CoreVersion      string `json:"coreVersion,omitempty"`
	UpdateInProgress bool   `json:"updateInProgress"`
	// HaltedReason is set while the supervisor refuses to run a core. Empty means not halted.
	HaltedReason string `json:"haltedReason,omitempty"`
}

// Status reports the supervisor's view of the core.
func (s *Supervisor) Status(ctx context.Context) StatusReport {
	h := s.opts.Home
	st := readState(h)
	rep := StatusReport{CurrentVersion: currentVersion(h), Previous: st.Previous}
	s.mu.Lock()
	p := s.core
	rep.UpdateInProgress = s.busy
	rep.HaltedReason = s.halted
	s.mu.Unlock()
	if p != nil && !p.exited() {
		rep.CoreRunning = true
		rep.CorePid = p.pid
	}
	if addr, err := coreHealthAddr(s.cfg.Listen); err == nil {
		if hr, err := queryHealth(ctx, s.probe, addr); err == nil {
			rep.CoreHealthy = hr.Status == "ok"
			rep.CoreVersion = hr.Version
		}
	}
	return rep
}
