// Package api implements the rhymehub HTTP API: REST under /api/v1, the managed-app SDK under /api/v1/sdk,
// and the embedded dashboard. Contracts: docs/API_SPEC.md and docs/plans/web-api-assumptions.md.
package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/lafin716/rhymehub/internal/config"
	"github.com/lafin716/rhymehub/internal/domain"
	"github.com/lafin716/rhymehub/internal/store"
)

// CSRFHeader is required on cookie-authenticated state-changing requests.
const CSRFHeader = "X-Rhymehub-CSRF"

// SessionCookie is the browser session cookie name.
const SessionCookie = "rh_session"

// SessionTTL is the lifetime of a browser session.
const SessionTTL = 7 * 24 * time.Hour

// Options configures a Server.
type Options struct {
	Store   *store.Store
	Config  config.Config
	Version string
	Logger  *slog.Logger
	// Now returns the current time. Nil means time.Now. Tests inject a fixed clock.
	Now func() time.Time
	// Releaser runs the release for an approved task. Nil installs a stub that reports "not configured".
	Releaser Releaser
	// Supervisor forwards self-update requests. Nil means system update endpoints return 503.
	Supervisor Supervisor
	// Canceller stops worker processes on cancel. Nil means only the task state is changed.
	Canceller Canceller
}

// Server holds the shared state for all handlers.
type Server struct {
	st         *store.Store
	cfg        config.Config
	version    string
	log        *slog.Logger
	now        func() time.Time
	started    time.Time
	insecure   []*net.IPNet
	releaser   Releaser
	supervisor Supervisor
	canceller  Canceller
	loginLimit *rateLimiter
	draining   bool
	drainMu    sync.RWMutex
}

// New builds a Server. It validates the TLS allow-list up front so misconfiguration fails at startup.
func New(opts Options) (*Server, error) {
	if opts.Store == nil {
		return nil, errors.New("api: store is required")
	}
	nets, err := parseCIDRs(opts.Config.InsecureAllowedCIDRs)
	if err != nil {
		return nil, err
	}
	s := &Server{
		st:         opts.Store,
		cfg:        opts.Config,
		version:    opts.Version,
		log:        opts.Logger,
		now:        opts.Now,
		insecure:   nets,
		releaser:   opts.Releaser,
		supervisor: opts.Supervisor,
		canceller:  opts.Canceller,
		loginLimit: newRateLimiter(5, time.Minute),
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.releaser == nil {
		s.releaser = unconfiguredReleaser{}
	}
	s.started = s.now()
	return s, nil
}

func parseCIDRs(in []string) ([]*net.IPNet, error) {
	out := make([]*net.IPNet, 0, len(in))
	for _, c := range in {
		_, n, err := net.ParseCIDR(strings.TrimSpace(c))
		if err != nil {
			return nil, fmt.Errorf("api: invalid insecureAllowedCIDRs entry %q: %w", c, err)
		}
		out = append(out, n)
	}
	return out, nil
}

// SetDraining toggles drain mode. While draining, state-creating work endpoints should refuse new work.
func (s *Server) SetDraining(v bool) {
	s.drainMu.Lock()
	s.draining = v
	s.drainMu.Unlock()
}

// Draining reports whether drain mode is on.
func (s *Server) Draining() bool {
	s.drainMu.RLock()
	defer s.drainMu.RUnlock()
	return s.draining
}

// Handler returns the full HTTP handler: TLS policy and panic-safe routing around the mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.routes(mux)
	return s.tlsPolicy(mux)
}

// tlsPolicy rejects plain-HTTP requests from non-loopback peers unless the peer is in the allow-list.
func (s *Server) tlsPolicy(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil || s.peerAllowedPlain(r) {
			next.ServeHTTP(w, r)
			return
		}
		writeError(w, http.StatusForbidden, "tls_required", "plain HTTP is only allowed from loopback or allow-listed networks")
	})
}

func (s *Server) peerAllowedPlain(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	for _, n := range s.insecure {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// errorBody is the error envelope from docs/API_SPEC.md.
type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	var body errorBody
	body.Error.Code = code
	body.Error.Message = msg
	writeJSON(w, status, body)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeDomainError maps domain errors to the API error envelope.
func writeDomainError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, domain.ErrIllegalTransition):
		writeError(w, http.StatusConflict, "illegal_transition", err.Error())
	case errors.Is(err, domain.ErrStateConflict), errors.Is(err, domain.ErrDuplicate):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, domain.ErrInvalid):
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
	}
}

// decodeJSON reads a bounded JSON body into v. Unknown fields are rejected to catch client typos.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any, maxBytes int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return false
	}
	return true
}

// principal is the authenticated caller for one request.
type principal struct {
	User   domain.User
	Cookie bool // true when authenticated by the browser session cookie
}

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// authenticate resolves the caller from the session cookie or a bearer API token.
// It returns (nil, nil) when no credentials were presented.
func (s *Server) authenticate(ctx context.Context, r *http.Request) (*principal, error) {
	if c, err := r.Cookie(SessionCookie); err == nil && c.Value != "" {
		se, err := s.st.GetSession(ctx, hashToken(c.Value))
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return nil, errUnauthenticated
			}
			return nil, err
		}
		if !se.ExpiresAt.After(s.now()) {
			return nil, errUnauthenticated
		}
		u, err := s.st.GetUser(ctx, se.UserID)
		if err != nil || u.Disabled {
			return nil, errUnauthenticated
		}
		return &principal{User: u, Cookie: true}, nil
	}
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		raw := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
		if !strings.HasPrefix(raw, "rht_") {
			return nil, errUnauthenticated
		}
		tok, err := s.st.GetAPITokenByHash(ctx, hashToken(raw))
		if err != nil || tok.RevokedAt != nil {
			return nil, errUnauthenticated
		}
		u, err := s.st.GetUser(ctx, tok.UserID)
		if err != nil || u.Disabled {
			return nil, errUnauthenticated
		}
		_ = s.st.TouchAPIToken(ctx, tok.ID, s.now())
		return &principal{User: u}, nil
	}
	return nil, nil
}

var errUnauthenticated = errors.New("unauthenticated")

// handler wraps an authenticated endpoint. min is the minimum role; empty means any authenticated user.
type handler func(w http.ResponseWriter, r *http.Request, p *principal)

// need returns a handler that requires authentication and at least min role.
// State-changing methods on cookie sessions must carry the CSRF header.
func (s *Server) need(min domain.Role, h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := s.authenticate(r.Context(), r)
		if err != nil {
			if errors.Is(err, errUnauthenticated) {
				writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
				return
			}
			s.log.Error("auth lookup failed", "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		if p == nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		if p.Cookie && isStateChanging(r.Method) && r.Header.Get(CSRFHeader) != "1" {
			writeError(w, http.StatusForbidden, "forbidden", "missing "+CSRFHeader+" header")
			return
		}
		if min != "" && !p.User.Role.AtLeast(min) {
			writeError(w, http.StatusForbidden, "forbidden", "requires role "+string(min))
			return
		}
		h(w, r, p)
	}
}

func isStateChanging(m string) bool {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

// rateLimiter is a fixed-window limiter keyed by string (for example client IP).
type rateLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	hits   map[string]windowCount
}

type windowCount struct {
	start time.Time
	n     int
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{limit: limit, window: window, hits: make(map[string]windowCount)}
}

// Allow records one hit for key at now and reports whether it is within the limit.
func (l *rateLimiter) Allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	c := l.hits[key]
	if now.Sub(c.start) >= l.window {
		c = windowCount{start: now}
	}
	c.n++
	l.hits[key] = c
	return c.n <= l.limit
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// health is the /healthz payload (docs/API_SPEC.md, Public).
func (s *Server) health() map[string]any {
	return map[string]any{
		"status":        "ok",
		"version":       s.version,
		"schemaVersion": s.schemaVersion(),
		"draining":      s.Draining(),
		"uptimeSec":     int(s.now().Sub(s.started).Seconds()),
	}
}

func (s *Server) schemaVersion() int {
	v, err := s.st.SchemaVersion(context.Background())
	if err != nil {
		return 0
	}
	return v
}
