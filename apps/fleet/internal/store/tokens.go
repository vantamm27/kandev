package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ErrTokenInvalid covers an unknown, expired, or already used registration
// token. The cases are deliberately indistinguishable to the caller.
var ErrTokenInvalid = errors.New("registration token is invalid, expired, or already used")

// RegistrationToken is a pending one-time token.
type RegistrationToken struct {
	OwnerUserID string
	Labels      []string
	ExpiresAt   time.Time
}

// HashToken returns the stored form of a raw token.
func HashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// CreateToken stores the hash of raw with its owner, labels, and expiry.
func (s *Store) CreateToken(ctx context.Context, raw string, tok RegistrationToken, now time.Time) error {
	labels, err := json.Marshal(nonNilLabels(tok.Labels))
	if err != nil {
		return fmt.Errorf("encode labels: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO registration_tokens
		(token_hash, owner_user_id, labels, expires_at, created_at) VALUES (?, ?, ?, ?, ?)`,
		HashToken(raw), tok.OwnerUserID, string(labels), tok.ExpiresAt.UTC(), now.UTC())
	if err != nil {
		return fmt.Errorf("insert registration token: %w", err)
	}
	return nil
}

// LookupToken returns the token if it is unused and unexpired at now.
func (s *Store) LookupToken(ctx context.Context, raw string, now time.Time) (RegistrationToken, error) {
	var tok RegistrationToken
	var labels string
	err := s.db.QueryRowContext(ctx, `SELECT owner_user_id, labels, expires_at FROM registration_tokens
		WHERE token_hash = ? AND used_at IS NULL AND expires_at > ?`, HashToken(raw), now.UTC()).
		Scan(&tok.OwnerUserID, &labels, &tok.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return RegistrationToken{}, ErrTokenInvalid
	}
	if err != nil {
		return RegistrationToken{}, fmt.Errorf("read registration token: %w", err)
	}
	if err := json.Unmarshal([]byte(labels), &tok.Labels); err != nil {
		return RegistrationToken{}, fmt.Errorf("decode token labels: %w", err)
	}
	return tok, nil
}

func nonNilLabels(labels []string) []string {
	if labels == nil {
		return []string{}
	}
	return labels
}
