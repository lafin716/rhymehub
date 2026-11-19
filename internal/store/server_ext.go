package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lafin716/rhymehub/internal/domain"
)

// ListTasksByRequest returns the tasks created from a request, oldest first.
func (s *Store) ListTasksByRequest(ctx context.Context, requestID string) ([]domain.Task, error) {
	if requestID == "" {
		return nil, fmt.Errorf("%w: request id is empty", domain.ErrInvalid)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+taskSelect+` FROM tasks
		WHERE request_id = ? ORDER BY created_at, rowid`, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SDKIdempotencyRecord remembers the request created for an SDK Idempotency-Key. Hash is a digest of the
// request body, so a key reused with a different body is rejected instead of silently deduped.
type SDKIdempotencyRecord struct {
	RequestID string    `json:"requestId"`
	Hash      string    `json:"hash"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// SDKIdempotencyTTL is how long an SDK idempotency key dedupes requests.
const SDKIdempotencyTTL = 24 * time.Hour

func sdkIdemKey(projectID, idemKey string) string {
	sum := sha256.Sum256([]byte(projectID + "\x00" + idemKey))
	return "sdk_idem:" + hex.EncodeToString(sum[:])
}

// GetSDKIdempotency returns the record for (project, key) when it exists and has not expired at now.
// The second result is false when there is no live record.
func (s *Store) GetSDKIdempotency(ctx context.Context, projectID, idemKey string, now time.Time) (SDKIdempotencyRecord, bool, error) {
	raw, err := s.GetSetting(ctx, sdkIdemKey(projectID, idemKey))
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return SDKIdempotencyRecord{}, false, nil
		}
		return SDKIdempotencyRecord{}, false, err
	}
	var rec SDKIdempotencyRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		return SDKIdempotencyRecord{}, false, fmt.Errorf("store: decode sdk idempotency: %w", err)
	}
	if !rec.ExpiresAt.After(now) {
		return SDKIdempotencyRecord{}, false, nil
	}
	return rec, true, nil
}

// PutSDKIdempotency stores a record for (project, key) with a 24 h lifetime from now. It overwrites an
// expired record. Callers must check GetSDKIdempotency first; the check and the put are not one transaction.
func (s *Store) PutSDKIdempotency(ctx context.Context, projectID, idemKey string, requestID, bodyHash string, now time.Time) error {
	if requestID == "" || bodyHash == "" {
		return fmt.Errorf("%w: idempotency record needs request id and body hash", domain.ErrInvalid)
	}
	rec := SDKIdempotencyRecord{RequestID: requestID, Hash: bodyHash, ExpiresAt: now.Add(SDKIdempotencyTTL).UTC()}
	b, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("store: encode sdk idempotency: %w", err)
	}
	return s.SetSetting(ctx, sdkIdemKey(projectID, idemKey), string(b))
}
