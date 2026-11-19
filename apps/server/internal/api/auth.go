package api

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/lafin716/rhymehub/internal/domain"
)

// bcryptCost is the password hashing cost (ADR/SECURITY: cost 12).
const bcryptCost = 12

const (
	minPasswordLen = 8
	maxPasswordLen = 72 // bcrypt only uses the first 72 bytes; longer input is rejected rather than truncated
	maxBodyBytes   = 64 << 10
)

// hashPassword returns a bcrypt hash of pw.
func hashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcryptCost)
	return string(b), err
}

func validatePassword(pw string) error {
	if len(pw) < minPasswordLen {
		return fmt.Errorf("%w: password must be at least %d characters", domain.ErrInvalid, minPasswordLen)
	}
	if len(pw) > maxPasswordLen {
		return fmt.Errorf("%w: password must be at most %d bytes", domain.ErrInvalid, maxPasswordLen)
	}
	return nil
}

func validateUsername(u string) error {
	if u == "" || len(u) > 64 || strings.ContainsAny(u, " \t\r\n") {
		return fmt.Errorf("%w: username must be 1-64 characters without whitespace", domain.ErrInvalid)
	}
	return nil
}

// dummyHash is compared against when the username is unknown, so login timing does not reveal accounts.
var (
	dummyOnce sync.Once
	dummyHash string
)

func getDummyHash() string {
	dummyOnce.Do(func() {
		dummyHash, _ = hashPassword("rhymehub-timing-dummy")
	})
	return dummyHash
}

func newRawToken(prefix string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// audit appends an audit entry for an authenticated action. Failures are logged, not returned, so an audit
// write problem cannot change the outcome of an already-decided request; mutating handlers check the store
// separately.
func (s *Server) audit(r *http.Request, actor *domain.User, action, targetType, targetID string, detail any) {
	e := domain.AuditEntry{At: s.now(), Action: action, TargetType: targetType, TargetID: targetID, IP: clientIP(r)}
	if actor != nil {
		e.Actor = actor.Username
		e.ActorRole = string(actor.Role)
	}
	if detail != nil {
		if b, err := json.Marshal(detail); err == nil {
			e.Detail = b
		}
	}
	if _, err := s.st.AppendAudit(r.Context(), e); err != nil {
		s.log.Error("audit append failed", "action", action, "err", err)
	}
}

// ---- Public ----

func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	n, err := s.st.CountUsers(r.Context())
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": s.version, "needsSetup": n == 0})
}

// handleSetup creates the first (admin) user. It only works while no user exists. It does not create a
// session; the client logs in afterwards (web assumption 1).
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &in, maxBodyBytes) {
		return
	}
	if err := validateUsername(in.Username); err != nil {
		writeDomainError(w, err)
		return
	}
	if err := validatePassword(in.Password); err != nil {
		writeDomainError(w, err)
		return
	}
	hash, err := hashPassword(in.Password)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	// Check-then-create: two concurrent setups can both pass the count check. The unique username index and
	// this count check together keep this acceptable for a single-operator install; see the report.
	n, err := s.st.CountUsers(r.Context())
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if n > 0 {
		writeError(w, http.StatusConflict, "conflict", "setup already completed")
		return
	}
	u, err := s.st.CreateUser(r.Context(), domain.User{Username: in.Username, PasswordHash: hash, Role: domain.RoleAdmin})
	if err != nil {
		writeDomainError(w, err)
		return
	}
	s.audit(r, &u, "setup.complete", "user", u.ID, nil)
	writeJSON(w, http.StatusCreated, map[string]any{"user": publicUser(u)})
}

// ---- Auth ----

func publicUser(u domain.User) map[string]any {
	return map[string]any{"id": u.ID, "username": u.Username, "role": u.Role, "createdAt": u.CreatedAt}
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	now := s.now()
	if !s.loginLimit.Allow(clientIP(r), now) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many login attempts")
		return
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &in, maxBodyBytes) {
		return
	}
	u, err := s.st.GetUserByUsername(r.Context(), in.Username)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		writeDomainError(w, err)
		return
	}
	found := err == nil
	hash := getDummyHash()
	if found {
		hash = u.PasswordHash
	}
	// Always run one bcrypt compare so unknown users and wrong passwords take the same time.
	pwErr := bcrypt.CompareHashAndPassword([]byte(hash), []byte(in.Password))
	if !found || pwErr != nil || u.Disabled {
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid username or password")
		return
	}
	raw, err := newRawToken("")
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if err := s.st.CreateSession(r.Context(), domain.Session{
		TokenHash: hashToken(raw), UserID: u.ID, CreatedAt: now, ExpiresAt: now.Add(SessionTTL),
	}); err != nil {
		writeDomainError(w, err)
		return
	}
	http.SetCookie(w, s.sessionCookie(r, raw, now.Add(SessionTTL)))
	s.audit(r, &u, "auth.login", "user", u.ID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"user": publicUser(u)})
}

// sessionCookie builds the rh_session cookie: HttpOnly, SameSite=Lax, Secure when the request is TLS.
func (s *Server) sessionCookie(r *http.Request, value string, expires time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     SessionCookie,
		Value:    value,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
	}
}

// handleLogout deletes the session if one is presented and always clears the cookie.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(SessionCookie); err == nil && c.Value != "" {
		if r.Header.Get(CSRFHeader) != "1" {
			writeError(w, http.StatusForbidden, "forbidden", "missing "+CSRFHeader+" header")
			return
		}
		if err := s.st.DeleteSession(r.Context(), hashToken(c.Value)); err != nil {
			writeDomainError(w, err)
			return
		}
	}
	http.SetCookie(w, s.sessionCookie(r, "", time.Unix(0, 0)))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request, p *principal) {
	writeJSON(w, http.StatusOK, map[string]any{"user": publicUser(p.User)})
}

// ---- API tokens ----

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request, p *principal) {
	toks, err := s.st.ListAPITokens(r.Context(), p.User.ID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toks)
}

func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request, p *principal) {
	var in struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &in, maxBodyBytes) {
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 100 {
		writeError(w, http.StatusBadRequest, "bad_request", "token name must be 1-100 characters")
		return
	}
	raw, err := newRawToken("rht_")
	if err != nil {
		writeDomainError(w, err)
		return
	}
	tok, err := s.st.CreateAPIToken(r.Context(), domain.APIToken{
		UserID: p.User.ID, Name: name, TokenHash: hashToken(raw), CreatedAt: s.now(),
	})
	if err != nil {
		writeDomainError(w, err)
		return
	}
	s.audit(r, &p.User, "token.create", "api_token", tok.ID, map[string]string{"name": name})
	// The raw token is returned exactly once.
	writeJSON(w, http.StatusCreated, map[string]any{"token": raw, "id": tok.ID})
}

func (s *Server) handleRevokeToken(w http.ResponseWriter, r *http.Request, p *principal) {
	id := r.PathValue("id")
	toks, err := s.st.ListAPITokens(r.Context(), p.User.ID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	owned := false
	for _, t := range toks {
		if t.ID == id {
			owned = true
			break
		}
	}
	if !owned {
		writeError(w, http.StatusNotFound, "not_found", "token not found")
		return
	}
	if err := s.st.RevokeAPIToken(r.Context(), id, s.now()); err != nil {
		writeDomainError(w, err)
		return
	}
	s.audit(r, &p.User, "token.revoke", "api_token", id, nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---- Users (admin) ----

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request, p *principal) {
	us, err := s.st.ListUsers(r.Context())
	if err != nil {
		writeDomainError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(us))
	for _, u := range us {
		out = append(out, publicUser(u))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request, p *principal) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if !decodeJSON(w, r, &in, maxBodyBytes) {
		return
	}
	role, err := domain.ParseRole(in.Role)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if err := validateUsername(in.Username); err != nil {
		writeDomainError(w, err)
		return
	}
	if err := validatePassword(in.Password); err != nil {
		writeDomainError(w, err)
		return
	}
	hash, err := hashPassword(in.Password)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	u, err := s.st.CreateUser(r.Context(), domain.User{Username: in.Username, PasswordHash: hash, Role: role})
	if err != nil {
		writeDomainError(w, err)
		return
	}
	s.audit(r, &p.User, "user.create", "user", u.ID, map[string]string{"username": u.Username, "role": string(role)})
	writeJSON(w, http.StatusCreated, publicUser(u))
}

func (s *Server) handlePatchUser(w http.ResponseWriter, r *http.Request, p *principal) {
	var in struct {
		Role     *string `json:"role"`
		Disabled *bool   `json:"disabled"`
		Password *string `json:"password"`
	}
	if !decodeJSON(w, r, &in, maxBodyBytes) {
		return
	}
	u, err := s.st.GetUser(r.Context(), r.PathValue("id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	changes := map[string]any{}
	if in.Role != nil {
		role, err := domain.ParseRole(*in.Role)
		if err != nil {
			writeDomainError(w, err)
			return
		}
		if u.ID == p.User.ID && role != domain.RoleAdmin {
			writeError(w, http.StatusBadRequest, "bad_request", "admins cannot demote themselves")
			return
		}
		u.Role = role
		changes["role"] = string(role)
	}
	if in.Disabled != nil {
		if u.ID == p.User.ID && *in.Disabled {
			writeError(w, http.StatusBadRequest, "bad_request", "admins cannot disable themselves")
			return
		}
		u.Disabled = *in.Disabled
		changes["disabled"] = *in.Disabled
	}
	if in.Password != nil {
		if err := validatePassword(*in.Password); err != nil {
			writeDomainError(w, err)
			return
		}
		h, err := hashPassword(*in.Password)
		if err != nil {
			writeDomainError(w, err)
			return
		}
		u.PasswordHash = h
		changes["password"] = "changed"
	}
	if err := s.st.UpdateUser(r.Context(), u); err != nil {
		writeDomainError(w, err)
		return
	}
	s.audit(r, &p.User, "user.update", "user", u.ID, changes)
	writeJSON(w, http.StatusOK, publicUser(u))
}
