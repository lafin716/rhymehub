package supervisor

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// maxAPIBody caps request bodies on the supervisor API.
const maxAPIBody = 4 << 10

// Handler returns the loopback control API. Every route requires `Authorization: Bearer <token>`,
// and that check runs before the method check so unauthenticated callers learn nothing.
//
//	GET  /status    supervisor and core status
//	POST /update    {"version": "x.y.z"}; responds with the UpdateRecord
//	GET  /updates   update history plus the in-flight journal, if any
//	POST /rollback  switch back to the previous version
func (s *Supervisor) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/status", s.route(http.MethodGet, s.handleStatus))
	mux.HandleFunc("/updates", s.route(http.MethodGet, s.handleUpdates))
	mux.HandleFunc("/update", s.route(http.MethodPost, s.handleUpdate))
	mux.HandleFunc("/rollback", s.route(http.MethodPost, s.handleRollback))
	return mux
}

// route wraps a handler with the bearer check and the expected method.
func (s *Supervisor) route(method string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="rhymehub-supervisor"`)
			writeAPIError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if r.Method != method {
			w.Header().Set("Allow", method)
			writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		next(w, r)
	}
}

// authorized compares the presented token in constant time.
func (s *Supervisor) authorized(r *http.Request) bool {
	const prefix = "Bearer "
	got := r.Header.Get("Authorization")
	if !strings.HasPrefix(got, prefix) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(got, prefix)), []byte(s.token)) == 1
}

func (s *Supervisor) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeAPIJSON(w, http.StatusOK, s.Status(r.Context()))
}

type updateRequest struct {
	Version string `json:"version"`
}

func (s *Supervisor) handleUpdate(w http.ResponseWriter, r *http.Request) {
	var req updateRequest
	if err := decodeAPIBody(r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	// The update runs under the supervisor's base context, so a dropped client connection does not
	// abort it partway through.
	rec, err := s.Update(s.baseCtx(), req.Version)
	writeOutcome(w, rec, err)
}

// writeOutcome answers an update or rollback. Success is 200 with the record. A rolled-back update
// is 409 and a failed one is 500, and the record is in the body either way, so the caller can see
// the phase and error. Requests refused before they began get 409 when busy or when a rollback has
// no database backup to restore, and 400 otherwise.
func writeOutcome(w http.ResponseWriter, rec UpdateRecord, err error) {
	switch {
	case errors.Is(err, ErrBusy), errors.Is(err, ErrNoBackupRollback):
		writeAPIError(w, http.StatusConflict, err.Error())
	case rec.ID == "":
		writeAPIError(w, http.StatusBadRequest, errorText(err))
	case err == nil:
		writeAPIJSON(w, http.StatusOK, rec)
	case rec.Status == StatusRolledBack:
		writeAPIJSON(w, http.StatusConflict, rec)
	default:
		writeAPIJSON(w, http.StatusInternalServerError, rec)
	}
}

func (s *Supervisor) handleUpdates(w http.ResponseWriter, r *http.Request) {
	history, err := ReadHistory(s.opts.Home.UpdatesLogPath())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	inFlight, err := ReadJournal(s.opts.Home.JournalPath())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]any{
		"updates":  history,
		"inFlight": inFlight,
	})
}

func (s *Supervisor) handleRollback(w http.ResponseWriter, r *http.Request) {
	rec, err := s.Rollback(s.baseCtx())
	writeOutcome(w, rec, err)
}

// decodeAPIBody reads a size-capped JSON body. An empty body decodes to the zero value.
func decodeAPIBody(r *http.Request, v any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxAPIBody+1))
	if err != nil {
		return err
	}
	if len(body) > maxAPIBody {
		return errors.New("request body too large")
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return errors.New("request body must be JSON with a version")
	}
	if err := json.Unmarshal(body, v); err != nil {
		return errors.New("invalid JSON body")
	}
	if req, ok := v.(*updateRequest); ok && req.Version == "" {
		return errors.New("version is required")
	}
	return nil
}

func errorText(err error) string {
	if err == nil {
		return "request refused"
	}
	return err.Error()
}

func writeAPIJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeAPIError(w http.ResponseWriter, code int, msg string) {
	writeAPIJSON(w, code, map[string]string{"error": msg})
}
