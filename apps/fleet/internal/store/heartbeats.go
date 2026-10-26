package store

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrStaleSignature means a signed request's timestamp is not newer than the
// last one accepted for the seat, so it may be a replay.
var ErrStaleSignature = errors.New("request timestamp is not newer than the last accepted one")

// HeartbeatUpdate is the seat data a heartbeat refreshes.
type HeartbeatUpdate struct {
	State        string
	LimitReason  string
	Quota5hPct   *float64
	QuotaWeekPct *float64
	Reset5hAt    *time.Time
	ResetWeekAt  *time.Time
	FreeSlots    int
	AgentVersion string
	SignedAt     int64
}

// ApplyHeartbeat stores a heartbeat if its signed timestamp is newer than the
// last accepted one, which makes a captured request useless to replay.
func (s *Store) ApplyHeartbeat(ctx context.Context, seatID string, u HeartbeatUpdate, now time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE seats SET state = ?, limit_reason = ?, quota_5h_pct = ?,
		quota_week_pct = ?, reset_5h_at = ?, reset_week_at = ?, free_slots = ?, agent_version = ?,
		last_signed_at = ?, last_heartbeat_at = ?, updated_at = ?
		WHERE id = ? AND last_signed_at < ?`,
		u.State, u.LimitReason, u.Quota5hPct, u.QuotaWeekPct, utcOrNil(u.Reset5hAt), utcOrNil(u.ResetWeekAt),
		u.FreeSlots, u.AgentVersion, u.SignedAt, now.UTC(), now.UTC(), seatID, u.SignedAt)
	if err != nil {
		return fmt.Errorf("apply heartbeat: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("apply heartbeat: %w", err)
	} else if n == 1 {
		return nil
	}
	if _, err := s.GetSeat(ctx, seatID); err != nil {
		return err
	}
	return ErrStaleSignature
}

// MarkStaleSeatsOffline moves seats whose last heartbeat (or registration,
// for a seat that never sent one) is older than cutoff to offline. Draining,
// retired, and already offline seats are left alone.
func (s *Store) MarkStaleSeatsOffline(ctx context.Context, cutoff, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE seats SET state = ?, updated_at = ?
		WHERE state NOT IN (?, ?, ?) AND COALESCE(last_heartbeat_at, created_at) < ?`,
		SeatOffline, now.UTC(), SeatOffline, SeatRetired, SeatDraining, cutoff.UTC())
	if err != nil {
		return 0, fmt.Errorf("mark stale seats offline: %w", err)
	}
	return res.RowsAffected()
}

func utcOrNil(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC()
}
