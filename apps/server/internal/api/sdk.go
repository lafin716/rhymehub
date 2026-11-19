package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lafin716/rhymehub/internal/domain"
	"github.com/lafin716/rhymehub/internal/redact"
	"github.com/lafin716/rhymehub/internal/store"
)

// SDK limits and headers (docs/API_SPEC.md, SDK section).
const (
	sdkKeyHeader          = "X-Rhymehub-Key"
	sdkIdempotencyHeader  = "Idempotency-Key"
	sdkKeyPrefix          = "rhk_"
	sdkRatePerMinute      = 30
	sdkMaxBodyBytes       = 6 << 20
	sdkMaxDiagnostics     = 20
	sdkMaxAttachments     = 5
	sdkMaxAttachmentBytes = 2 << 20
	sdkMaxAttachmentsSum  = 4 << 20
	sdkMaxInstallationLen = 128
	sdkListLimit          = 100
)

var sdkScreenshotMimes = map[string]bool{"image/png": true, "image/jpeg": true, "image/webp": true}

// sdkRequestView is the SdkRequest shape. It never includes prompts, worktree paths, diffs or internal errors.
type sdkRequestView struct {
	ID          string               `json:"id"`
	ProjectID   string               `json:"projectId"`
	Type        domain.RequestType   `json:"type"`
	Title       string               `json:"title"`
	Description string               `json:"description"`
	Status      domain.RequestStatus `json:"status"`
	CreatedAt   time.Time            `json:"createdAt"`
	UpdatedAt   time.Time            `json:"updatedAt"`
	Progress    domain.Progress      `json:"progress"`
	Question    string               `json:"question,omitempty"`
	Result      *sdkResult           `json:"result,omitempty"`
	Release     *sdkRelease          `json:"release,omitempty"`
}

type sdkResult struct {
	Summary    string `json:"summary"`
	DiffStat   string `json:"diffStat"`
	TestStatus string `json:"testStatus"`
}

type sdkRelease struct {
	Version string               `json:"version"`
	Status  domain.ReleaseStatus `json:"status"`
}

type sdkDiagnosticBody struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Content string `json:"content"`
}

type sdkAttachmentBody struct {
	Kind       string `json:"kind"`
	Filename   string `json:"filename"`
	Mime       string `json:"mime"`
	DataBase64 string `json:"dataBase64"`
}

type sdkCreateBody struct {
	Type           string              `json:"type"`
	Title          string              `json:"title"`
	Description    string              `json:"description"`
	InstallationID string              `json:"installationId"`
	AppVersion     string              `json:"appVersion"`
	Platform       string              `json:"platform"`
	Diagnostics    []sdkDiagnosticBody `json:"diagnostics"`
	Attachments    []sdkAttachmentBody `json:"attachments"`
}

type sdkStagedAttachment struct {
	att  domain.Attachment
	data []byte
}

// sdkAuth authenticates the X-Rhymehub-Key header, applies the key's origin policy and records the use.
// It writes the error response and returns false on failure.
func (s *Server) sdkAuth(w http.ResponseWriter, r *http.Request) (domain.ProjectKey, bool) {
	raw := strings.TrimSpace(r.Header.Get(sdkKeyHeader))
	if !strings.HasPrefix(raw, sdkKeyPrefix) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "reporter key required")
		return domain.ProjectKey{}, false
	}
	ctx := r.Context()
	k, err := s.st.GetProjectKeyByHash(ctx, hashToken(raw))
	if errors.Is(err, domain.ErrNotFound) || (err == nil && k.RevokedAt != nil) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "reporter key is invalid or revoked")
		return domain.ProjectKey{}, false
	}
	if err != nil {
		s.log.Error("sdk key lookup", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
		return domain.ProjectKey{}, false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		if !originAllowed(k.AllowedOrigins, origin) {
			writeError(w, http.StatusForbidden, "forbidden", "origin is not allowed for this key")
			return domain.ProjectKey{}, false
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Add("Vary", "Origin")
	}
	if err := s.st.TouchProjectKey(ctx, k.ID, s.now()); err != nil {
		s.log.Error("sdk key touch", "key", k.ID, "err", err)
	}
	return k, true
}

func originAllowed(allowed []string, origin string) bool {
	want := strings.TrimRight(origin, "/")
	for _, a := range allowed {
		if a == "*" || strings.EqualFold(strings.TrimRight(a, "/"), want) {
			return true
		}
	}
	return false
}

// handleSDKPreflight answers CORS preflight for /api/v1/sdk/*. The key is checked on the real request,
// which is refused when its origin is not in the key's AllowedOrigins.
func (s *Server) handleSDKPreflight(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", sdkKeyHeader+", Content-Type, "+sdkIdempotencyHeader)
		w.Header().Set("Access-Control-Max-Age", "600")
		w.Header().Add("Vary", "Origin")
	}
	w.WriteHeader(http.StatusNoContent)
}

// sdkInstallation validates the installationId; it writes 400 and returns false when it is unusable.
func sdkInstallation(w http.ResponseWriter, raw string) (string, bool) {
	inst := strings.TrimSpace(raw)
	if inst == "" || len(inst) > sdkMaxInstallationLen {
		writeError(w, http.StatusBadRequest, "bad_request", "installationId is required (max 128 bytes)")
		return "", false
	}
	return inst, true
}

// sdkAllow applies the per key+installation limit (30 requests per minute). It writes 429 when exceeded.
func (s *Server) sdkAllow(w http.ResponseWriter, k domain.ProjectKey, inst string) bool {
	if s.sdkLimit.Allow(k.ID+"|"+inst, s.now()) {
		return true
	}
	w.Header().Set("Retry-After", "60")
	writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests for this installation")
	return false
}

// readSDKBody reads the body under the 6 MiB cap. An oversized body is reported as bad_request.
func readSDKBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, sdkMaxBodyBytes)
	b, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "request body is too large or unreadable")
		return nil, false
	}
	return b, true
}

func decodeSDKBody(w http.ResponseWriter, raw []byte, v any) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return false
	}
	return true
}

// sdkView builds the SdkRequest for one request. Progress follows the newest linked task.
func (s *Server) sdkView(ctx context.Context, rq domain.Request) (sdkRequestView, error) {
	v := sdkRequestView{
		ID: rq.ID, ProjectID: rq.ProjectID, Type: rq.Type, Title: rq.Title, Description: rq.Description,
		Status: rq.Status, CreatedAt: rq.CreatedAt, UpdatedAt: rq.UpdatedAt,
		Progress: domain.ProgressFor(domain.StateCreated),
	}
	if rq.Status == domain.RequestRejected {
		v.Progress = domain.ProgressFor(domain.StateCancelled)
	}
	tasks, err := s.st.ListTasksByRequest(ctx, rq.ID)
	if err != nil {
		return v, err
	}
	if len(tasks) == 0 {
		return v, nil
	}
	t := tasks[len(tasks)-1]
	v.Progress = domain.ProgressFor(t.State)
	switch t.State {
	case domain.StateWaitingInput:
		v.Question = t.PendingQuestion
	case domain.StateReviewReady, domain.StateApproved, domain.StateMerging, domain.StateReleasing,
		domain.StateCompleted, domain.StateRolledBack:
		v.Result = &sdkResult{Summary: t.ResultSummary, DiffStat: t.DiffStat, TestStatus: t.TestStatus}
	}
	if t.ReleaseID != "" {
		rel, err := s.st.GetRelease(ctx, t.ReleaseID)
		switch {
		case err == nil:
			v.Release = &sdkRelease{Version: rel.Version, Status: rel.Status}
		case !errors.Is(err, domain.ErrNotFound):
			return v, err
		}
	}
	return v, nil
}

func (s *Server) writeSDKView(w http.ResponseWriter, ctx context.Context, rq domain.Request, status int) {
	v, err := s.sdkView(ctx, rq)
	if err != nil {
		s.log.Error("sdk view", "request", rq.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	writeJSON(w, status, v)
}

// sdkRequestFor loads a request only when it belongs to the key's project, came from the SDK and
// was filed by the same installation. Anything else is reported as not_found.
func (s *Server) sdkRequestFor(ctx context.Context, k domain.ProjectKey, id, inst string) (domain.Request, error) {
	rq, err := s.st.GetRequest(ctx, id)
	if err != nil {
		return domain.Request{}, err
	}
	if rq.ProjectID != k.ProjectID || rq.Source != domain.SourceSDK || rq.InstallationID != inst {
		return domain.Request{}, fmt.Errorf("%w: request %s", domain.ErrNotFound, id)
	}
	return rq, nil
}

func (s *Server) handleSDKCreateRequest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	k, ok := s.sdkAuth(w, r)
	if !ok {
		return
	}
	raw, ok := readSDKBody(w, r)
	if !ok {
		return
	}
	var body sdkCreateBody
	if !decodeSDKBody(w, raw, &body) {
		return
	}
	inst, ok := sdkInstallation(w, body.InstallationID)
	if !ok || !s.sdkAllow(w, k, inst) {
		return
	}
	idem := r.Header.Get(sdkIdempotencyHeader)
	if len(idem) > 128 {
		writeError(w, http.StatusBadRequest, "bad_request", "Idempotency-Key is too long")
		return
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	if idem != "" {
		rec, found, err := s.st.GetSDKIdempotency(ctx, k.ProjectID, idem, s.now())
		if err != nil {
			s.log.Error("sdk idempotency lookup", "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		if found {
			if rec.Hash != hash {
				writeError(w, http.StatusConflict, "conflict", "Idempotency-Key was already used with a different body")
				return
			}
			rq, err := s.st.GetRequest(ctx, rec.RequestID)
			if err != nil {
				writeDomainError(w, err)
				return
			}
			s.writeSDKView(w, ctx, rq, http.StatusOK)
			return
		}
	}
	if err := validateSDKCreate(&body); err != nil {
		writeDomainError(w, err)
		return
	}
	if len(body.Attachments) > 0 && s.paths.Root == "" {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "attachments are not configured on this server")
		return
	}
	proj, err := s.st.GetProject(ctx, k.ProjectID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if proj.Archived {
		writeError(w, http.StatusBadRequest, "bad_request", "project is archived")
		return
	}

	// Everything the reporter sends is redacted before it is stored. Redaction counts are kept on the request.
	redactions := 0
	desc, n := redact.Redact(strings.TrimSpace(body.Description))
	redactions += n
	title := strings.TrimSpace(body.Title)
	if title == "" {
		title = firstLine(desc, 120)
	} else {
		title, n = redact.Redact(firstLine(title, 200))
		redactions += n
	}
	diags := make([]domain.Diagnostic, 0, len(body.Diagnostics)+len(body.Attachments))
	for _, d := range body.Diagnostics {
		c, n := redact.Redact(d.Content)
		redactions += n
		diags = append(diags, domain.Diagnostic{Kind: d.Kind, Name: d.Name, Content: c})
	}
	staged, n, err := stageSDKAttachments(body.Attachments)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	redactions += n
	for _, st := range staged {
		diags = append(diags, domain.Diagnostic{Kind: string(st.att.Kind), Name: st.att.Filename, AttachmentID: st.att.ID})
	}

	reqID := domain.NewID("req")
	rq, err := s.st.CreateRequest(ctx, domain.Request{
		ID: reqID, ProjectID: proj.ID, Source: domain.SourceSDK, Type: domain.RequestType(body.Type),
		Title: title, Description: desc, Priority: defaultPriority, InstallationID: inst,
		AppVersion: strings.TrimSpace(body.AppVersion), Platform: strings.TrimSpace(body.Platform),
		ReporterKeyID: k.ID, Diagnostics: diags, Redactions: redactions,
	})
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if err := s.storeSDKAttachments(ctx, rq.ID, staged); err != nil {
		s.log.Error("sdk attachments", "request", rq.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	if idem != "" {
		if err := s.st.PutSDKIdempotency(ctx, k.ProjectID, idem, rq.ID, hash, s.now()); err != nil {
			s.log.Error("sdk idempotency store", "request", rq.ID, "err", err)
		}
	}
	s.log.Info("sdk request created", "request", rq.ID, "project", proj.ID, "key", k.ID)
	s.writeSDKView(w, ctx, rq, http.StatusCreated)
}

// validateSDKCreate checks the SDK create body. Release requests cannot be filed by reporters.
func validateSDKCreate(b *sdkCreateBody) error {
	rt := domain.RequestType(b.Type)
	if !rt.Valid() || rt == domain.RequestRelease {
		return fmt.Errorf("%w: type must be a reporter request type", domain.ErrInvalid)
	}
	if strings.TrimSpace(b.Description) == "" || len(b.Description) > maxTextBytes {
		return fmt.Errorf("%w: description is required (max %d bytes)", domain.ErrInvalid, maxTextBytes)
	}
	if len(b.Title) > 200*4 {
		return fmt.Errorf("%w: title is too long", domain.ErrInvalid)
	}
	if len(b.AppVersion) > 64 || len(b.Platform) > 64 {
		return fmt.Errorf("%w: appVersion and platform are limited to 64 bytes", domain.ErrInvalid)
	}
	if len(b.Diagnostics) > sdkMaxDiagnostics {
		return fmt.Errorf("%w: at most %d diagnostics", domain.ErrInvalid, sdkMaxDiagnostics)
	}
	for _, d := range b.Diagnostics {
		switch d.Kind {
		case "log", "error", "context":
		default:
			return fmt.Errorf("%w: diagnostic kind must be log, error or context", domain.ErrInvalid)
		}
		if len(d.Name) == 0 || len(d.Name) > 128 {
			return fmt.Errorf("%w: diagnostic name must be 1..128 bytes", domain.ErrInvalid)
		}
		if len(d.Content) > maxDiagnosticPromptBytes {
			return fmt.Errorf("%w: diagnostic content exceeds %d bytes", domain.ErrInvalid, maxDiagnosticPromptBytes)
		}
	}
	if len(b.Attachments) > sdkMaxAttachments {
		return fmt.Errorf("%w: at most %d attachments", domain.ErrInvalid, sdkMaxAttachments)
	}
	return nil
}

// stageSDKAttachments validates and decodes attachments in memory. Log attachments are redacted and
// their redaction count is returned. Files are written only after the request row exists.
func stageSDKAttachments(in []sdkAttachmentBody) ([]sdkStagedAttachment, int, error) {
	var (
		out        []sdkStagedAttachment
		total      int
		redactions int
	)
	for _, a := range in {
		kind := domain.AttachKind(a.Kind)
		switch kind {
		case domain.AttachScreenshot:
			if !sdkScreenshotMimes[a.Mime] {
				return nil, 0, fmt.Errorf("%w: screenshot mime must be png, jpeg or webp", domain.ErrInvalid)
			}
		case domain.AttachLog:
			if a.Mime != "text/plain" {
				return nil, 0, fmt.Errorf("%w: log mime must be text/plain", domain.ErrInvalid)
			}
		default:
			return nil, 0, fmt.Errorf("%w: attachment kind must be screenshot or log", domain.ErrInvalid)
		}
		name := filepath.Base(strings.TrimSpace(a.Filename))
		if name == "" || name == "." || name == "/" || len(name) > 128 {
			return nil, 0, fmt.Errorf("%w: attachment filename is invalid", domain.ErrInvalid)
		}
		data, err := base64.StdEncoding.DecodeString(a.DataBase64)
		if err != nil {
			return nil, 0, fmt.Errorf("%w: attachment data is not base64", domain.ErrInvalid)
		}
		if len(data) == 0 || len(data) > sdkMaxAttachmentBytes {
			return nil, 0, fmt.Errorf("%w: attachment must be 1..%d bytes", domain.ErrInvalid, sdkMaxAttachmentBytes)
		}
		total += len(data)
		if total > sdkMaxAttachmentsSum {
			return nil, 0, fmt.Errorf("%w: attachments exceed %d bytes in total", domain.ErrInvalid, sdkMaxAttachmentsSum)
		}
		sniffed := http.DetectContentType(data)
		if kind == domain.AttachScreenshot && sniffed != a.Mime {
			return nil, 0, fmt.Errorf("%w: screenshot content does not match its mime type", domain.ErrInvalid)
		}
		if kind == domain.AttachLog {
			if !strings.HasPrefix(sniffed, "text/plain") {
				return nil, 0, fmt.Errorf("%w: log content is not text", domain.ErrInvalid)
			}
			red, n := redact.Redact(string(data))
			redactions += n
			data = []byte(red)
		}
		out = append(out, sdkStagedAttachment{
			att: domain.Attachment{
				ID: domain.NewID("att"), Kind: kind, Filename: name, Mime: a.Mime,
				Size: int64(len(data)), Redacted: kind == domain.AttachLog,
			},
			data: data,
		})
	}
	return out, redactions, nil
}

// storeSDKAttachments writes staged files under AttachmentsDir (outside worktrees, mode 0600) and records them.
// On failure it removes the files it wrote.
func (s *Server) storeSDKAttachments(ctx context.Context, requestID string, staged []sdkStagedAttachment) error {
	if len(staged) == 0 {
		return nil
	}
	dir := s.paths.AttachmentsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create attachments dir: %w", err)
	}
	var written []string
	fail := func(err error) error {
		for _, p := range written {
			_ = os.Remove(p)
		}
		return err
	}
	for _, st := range staged {
		a := st.att
		a.RequestID = requestID
		a.Path = filepath.Join(dir, a.ID)
		if err := os.WriteFile(a.Path, st.data, 0o600); err != nil {
			return fail(fmt.Errorf("write attachment: %w", err))
		}
		written = append(written, a.Path)
		if _, err := s.st.CreateAttachment(ctx, a); err != nil {
			return fail(fmt.Errorf("record attachment: %w", err))
		}
	}
	return nil
}

func (s *Server) handleSDKListRequests(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	k, ok := s.sdkAuth(w, r)
	if !ok {
		return
	}
	inst, ok := sdkInstallation(w, r.URL.Query().Get("installationId"))
	if !ok || !s.sdkAllow(w, k, inst) {
		return
	}
	rqs, err := s.st.ListRequests(ctx, k.ProjectID, "", 0)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	out := make([]sdkRequestView, 0)
	for _, rq := range rqs {
		if rq.Source != domain.SourceSDK || rq.InstallationID != inst {
			continue
		}
		v, err := s.sdkView(ctx, rq)
		if err != nil {
			writeDomainError(w, err)
			return
		}
		out = append(out, v)
		if len(out) == sdkListLimit {
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": out})
}

func (s *Server) handleSDKGetRequest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	k, ok := s.sdkAuth(w, r)
	if !ok {
		return
	}
	inst, ok := sdkInstallation(w, r.URL.Query().Get("installationId"))
	if !ok || !s.sdkAllow(w, k, inst) {
		return
	}
	rq, err := s.sdkRequestFor(ctx, k, r.PathValue("id"), inst)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	s.writeSDKView(w, ctx, rq, http.StatusOK)
}

type sdkAnswerBody struct {
	InstallationID string `json:"installationId"`
	Text           string `json:"text"`
}

// handleSDKAnswer accepts a reporter's answer only while the linked task is WAITING_INPUT.
func (s *Server) handleSDKAnswer(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	k, ok := s.sdkAuth(w, r)
	if !ok {
		return
	}
	raw, ok := readSDKBody(w, r)
	if !ok {
		return
	}
	var body sdkAnswerBody
	if !decodeSDKBody(w, raw, &body) {
		return
	}
	inst, ok := sdkInstallation(w, body.InstallationID)
	if !ok || !s.sdkAllow(w, k, inst) {
		return
	}
	if !validText(body.Text, maxTextBytes) {
		writeError(w, http.StatusBadRequest, "bad_request", "text is required")
		return
	}
	rq, err := s.sdkRequestFor(ctx, k, r.PathValue("id"), inst)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	tasks, err := s.st.ListTasksByRequest(ctx, rq.ID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if len(tasks) == 0 {
		writeError(w, http.StatusConflict, "conflict", "no task is waiting for an answer")
		return
	}
	t := tasks[len(tasks)-1]
	if t.State != domain.StateWaitingInput {
		writeError(w, http.StatusConflict, "conflict", "task is not waiting for an answer")
		return
	}
	text, _ := redact.Redact(body.Text)
	if _, err := s.transition(ctx, t.ID, []domain.TaskState{domain.StateWaitingInput}, domain.StateQueued,
		&store.EventInput{Reason: "question answered"}); err != nil {
		writeDomainError(w, err)
		return
	}
	if _, err := s.st.MutateTask(ctx, t.ID, func(t *domain.Task) error {
		t.NextInput = text
		t.PendingQuestion = ""
		return nil
	}); err != nil {
		writeDomainError(w, err)
		return
	}
	if _, err := s.st.AppendTaskEvent(ctx, t.ID, store.NewEvent{
		Type: domain.EventAnswer, Payload: rawJSON(map[string]any{"text": text, "author": "reporter"}),
	}); err != nil {
		s.log.Error("append sdk answer", "task", t.ID, "err", err)
	}
	s.writeSDKView(w, ctx, rq, http.StatusOK)
}

type sdkUpdate struct {
	Kind string `json:"kind"`
	URL  string `json:"url,omitempty"`
}

type sdkLatestRelease struct {
	LatestVersion   string     `json:"latestVersion"`
	UpdateAvailable bool       `json:"updateAvailable"`
	Notes           string     `json:"notes"`
	PublishedAt     *time.Time `json:"publishedAt"`
	Update          sdkUpdate  `json:"update"`
}

// updateKind maps a reporter platform to the update channel. Unknown platforms get "none".
func updateKind(platform string) string {
	switch platform {
	case "web":
		return "web-reload"
	case "ios", "android":
		return "store"
	case "electron", "macos", "windows", "linux", "desktop":
		return "electron-feed"
	}
	return "none"
}

// handleSDKLatestRelease reports the newest published release of the key's project.
func (s *Server) handleSDKLatestRelease(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	k, ok := s.sdkAuth(w, r)
	if !ok {
		return
	}
	// The release check is not tied to an installation, so the limiter uses a fixed pseudo-installation.
	if !s.sdkAllow(w, k, "release-check") {
		return
	}
	q := r.URL.Query()
	current := strings.TrimSpace(q.Get("currentVersion"))
	platform := strings.TrimSpace(q.Get("platform"))
	if len(current) > 64 || len(platform) > 32 {
		writeError(w, http.StatusBadRequest, "bad_request", "currentVersion or platform is too long")
		return
	}
	rels, err := s.st.ListReleases(ctx, k.ProjectID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	var latest *domain.Release
	for i := range rels {
		rel := &rels[i]
		if rel.Status != domain.ReleasePublished {
			continue
		}
		if latest == nil || rel.CreatedAt.After(latest.CreatedAt) {
			latest = rel
		}
	}
	resp := sdkLatestRelease{Update: sdkUpdate{Kind: "none"}}
	if latest != nil {
		published := latest.CreatedAt
		resp.LatestVersion = latest.Version
		resp.Notes = latest.Notes
		resp.PublishedAt = &published
		// Versions are compared as strings; there is no semver ordering in the store yet.
		resp.UpdateAvailable = current != "" && latest.Version != current
		if resp.UpdateAvailable {
			resp.Update.Kind = updateKind(platform)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}
