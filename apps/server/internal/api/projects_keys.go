package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/lafin716/rhymehub/internal/domain"
)

const (
	maxKeyNameLen    = 100
	maxKeyOrigins    = 20
	maxKeyOriginLen  = 200
	keyPrefixDisplay = 12 // "rhk_" plus 8 random characters shown in listings
)

// handleListKeys serves GET /projects/{id}/keys. Hashes are never returned (ProjectKey.KeyHash is json:"-").
func (s *Server) handleListKeys(w http.ResponseWriter, r *http.Request, _ *principal) {
	ctx := r.Context()
	id := r.PathValue("id")
	if _, err := s.st.GetProject(ctx, id); err != nil {
		writeDomainError(w, err)
		return
	}
	keys, err := s.st.ListProjectKeys(ctx, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if keys == nil {
		keys = []domain.ProjectKey{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": keys})
}

// handleCreateKey serves POST /projects/{id}/keys. The raw rhk_ key is returned once in the response.
func (s *Server) handleCreateKey(w http.ResponseWriter, r *http.Request, p *principal) {
	var in struct {
		Name           string   `json:"name"`
		AllowedOrigins []string `json:"allowedOrigins"`
	}
	if !decodeJSON(w, r, &in, maxBodyBytes) {
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > maxKeyNameLen {
		writeDomainError(w, badRequest("name must be 1-%d characters", maxKeyNameLen))
		return
	}
	if len(in.AllowedOrigins) > maxKeyOrigins {
		writeDomainError(w, badRequest("allowedOrigins may contain at most %d entries", maxKeyOrigins))
		return
	}
	for _, o := range in.AllowedOrigins {
		if o == "" || len(o) > maxKeyOriginLen {
			writeDomainError(w, badRequest("allowedOrigins entries must be 1-%d characters", maxKeyOriginLen))
			return
		}
	}
	ctx := r.Context()
	id := r.PathValue("id")
	if _, err := s.st.GetProject(ctx, id); err != nil {
		writeDomainError(w, err)
		return
	}
	raw, err := newRawToken(sdkKeyPrefix)
	if err != nil {
		writeDomainError(w, fmt.Errorf("generate key: %w", err))
		return
	}
	prefix := raw[:keyPrefixDisplay]
	created, err := s.st.CreateProjectKey(ctx, domain.ProjectKey{
		ProjectID:      id,
		Name:           name,
		KeyHash:        hashToken(raw),
		KeyPrefix:      prefix,
		AllowedOrigins: in.AllowedOrigins,
	})
	if err != nil {
		writeDomainError(w, err)
		return
	}
	s.audit(r, &p.User, "key.create", "project_key", created.ID,
		map[string]any{"projectId": id, "name": name, "keyPrefix": prefix})
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":             created.ID,
		"key":            raw,
		"keyPrefix":      created.KeyPrefix,
		"name":           created.Name,
		"projectId":      created.ProjectID,
		"allowedOrigins": created.AllowedOrigins,
		"createdAt":      created.CreatedAt,
	})
}

// handleRevokeKey serves DELETE /projects/{id}/keys/{keyId}. The key must belong to the project in the path.
func (s *Server) handleRevokeKey(w http.ResponseWriter, r *http.Request, p *principal) {
	ctx := r.Context()
	projectID := r.PathValue("id")
	keyID := r.PathValue("keyId")
	if _, err := s.st.GetProject(ctx, projectID); err != nil {
		writeDomainError(w, err)
		return
	}
	keys, err := s.st.ListProjectKeys(ctx, projectID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	var target *domain.ProjectKey
	for i := range keys {
		if keys[i].ID == keyID {
			target = &keys[i]
			break
		}
	}
	if target == nil {
		writeDomainError(w, fmt.Errorf("%w: key %s", domain.ErrNotFound, keyID))
		return
	}
	if target.RevokedAt != nil {
		writeDomainError(w, fmt.Errorf("%w: key %s is already revoked", domain.ErrStateConflict, keyID))
		return
	}
	if err := s.st.RevokeProjectKey(ctx, keyID, s.now()); err != nil {
		writeDomainError(w, err)
		return
	}
	s.audit(r, &p.User, "key.revoke", "project_key", keyID,
		map[string]any{"projectId": projectID, "keyPrefix": target.KeyPrefix})
	writeJSON(w, http.StatusOK, map[string]any{"id": keyID, "revoked": true})
}
