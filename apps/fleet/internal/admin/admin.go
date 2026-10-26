// Package admin implements seat administration: drain, undrain, retire, and
// secret rotation.
package admin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/kandev/kandev/apps/fleet/internal/kandev"
	"github.com/kandev/kandev/apps/fleet/internal/store"
)

// Kandev is the part of the Kandev client administration needs.
type Kandev interface {
	DeleteExecutor(ctx context.Context, id string) error
}

// ErrSeatBusy means a seat still runs sessions and cannot be retired yet.
var ErrSeatBusy = errors.New("seat still reports running sessions; wait for them to finish or pass force")

// KandevError wraps a failed Kandev call.
type KandevError struct{ Err error }

func (e *KandevError) Error() string { return "kandev: " + e.Err.Error() }
func (e *KandevError) Unwrap() error { return e.Err }

// Service administers seats.
type Service struct {
	store  *store.Store
	kandev Kandev
	log    *slog.Logger
	now    func() time.Time
}

// New returns an admin service.
func New(st *store.Store, k Kandev, log *slog.Logger) *Service {
	return &Service{store: st, kandev: k, log: log, now: time.Now}
}

// liveStates are every state a seat can be drained from.
var liveStates = []string{
	store.SeatRegistering, store.SeatOnline, store.SeatBusy,
	store.SeatCooldown5h, store.SeatCooldownWeek, store.SeatOffline,
}

// ListSeats returns every seat without secrets.
func (s *Service) ListSeats(ctx context.Context) ([]store.SeatSummary, error) {
	return s.store.ListSeats(ctx)
}

// Drain stops new work from going to a seat; running sessions continue.
func (s *Service) Drain(ctx context.Context, id string) error {
	if err := s.store.SetSeatState(ctx, id, liveStates, store.SeatDraining, s.now()); err != nil {
		return err
	}
	s.log.Info("seat draining", "seat_id", id)
	return nil
}

// Undrain returns a draining seat to service; its next heartbeat sets the
// exact state.
func (s *Service) Undrain(ctx context.Context, id string) error {
	if err := s.store.SetSeatState(ctx, id, []string{store.SeatDraining}, store.SeatOnline, s.now()); err != nil {
		return err
	}
	s.log.Info("seat back in service", "seat_id", id)
	return nil
}

// Retire deletes a drained seat's Kandev executor and marks it retired, which
// frees its endpoint for a new registration. Without force the seat must be
// draining and report every slot free.
func (s *Service) Retire(ctx context.Context, id string, force bool) error {
	seat, err := s.store.GetSeat(ctx, id)
	if err != nil {
		return err
	}
	if seat.State == store.SeatRetired {
		return store.ErrSeatStateConflict
	}
	if !force {
		if seat.State != store.SeatDraining {
			return store.ErrSeatStateConflict
		}
		free, err := s.store.FreeSlots(ctx, id)
		if err != nil {
			return err
		}
		if free < seat.Capacity {
			return ErrSeatBusy
		}
	}
	if seat.KandevExecutorID != "" {
		if err := s.kandev.DeleteExecutor(ctx, seat.KandevExecutorID); err != nil && !kandev.IsNotFound(err) {
			return &KandevError{Err: err}
		}
	}
	from := []string{seat.State}
	if err := s.store.SetSeatState(ctx, id, from, store.SeatRetired, s.now()); err != nil {
		return err
	}
	s.log.Info("seat retired", "seat_id", id, "kandev_executor_id", seat.KandevExecutorID, "forced", force)
	return nil
}

// RotateSecret issues a new HMAC secret for a live seat and returns it once.
// The node must be given the new secret (or re-registered) before its next
// heartbeat is accepted.
func (s *Service) RotateSecret(ctx context.Context, id string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random: %w", err)
	}
	secret := hex.EncodeToString(b)
	if err := s.store.RotateSeatSecret(ctx, id, secret, s.now()); err != nil {
		return "", err
	}
	s.log.Info("seat secret rotated", "seat_id", id)
	return secret, nil
}
