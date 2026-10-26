package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestOfferHandoffOncePerOpenOffer(t *testing.T) {
	s := registeredStore(t, newSeat("seat-1", "mini-1", "seat-a"))
	ctx := context.Background()
	h, err := s.OfferHandoff(ctx, "task-1", "seat-1", testNow)
	if err != nil || h.State != HandoffOffered || h.Attempt != 1 {
		t.Fatalf("offer = %+v, %v", h, err)
	}
	if _, err := s.OfferHandoff(ctx, "task-1", "seat-1", testNow); !errors.Is(err, ErrHandoffOpen) {
		t.Fatalf("second offer = %v", err)
	}
	if err := s.WithdrawHandoffs(ctx, "task-1", testNow); err != nil {
		t.Fatal(err)
	}
	h2, err := s.OfferHandoff(ctx, "task-1", "seat-1", testNow.Add(time.Hour))
	if err != nil || h2.Attempt != 1 {
		t.Fatalf("re-offer after withdrawal = %+v, %v; a withdrawn offer is not a handoff", h2, err)
	}
	offered, err := s.ListHandoffs(ctx, HandoffOffered)
	if err != nil || len(offered) != 1 || offered[0].ID != h2.ID {
		t.Fatalf("offered = %+v, %v", offered, err)
	}
	all, _ := s.ListHandoffs(ctx)
	if len(all) != 2 {
		t.Fatalf("all handoffs = %d", len(all))
	}
}

func TestOfferHandoffExhaustsAfterMax(t *testing.T) {
	s := registeredStore(t, newSeat("seat-1", "mini-1", "seat-a"))
	ctx := context.Background()
	for i := 0; i < MaxHandoffs; i++ {
		h, err := s.OfferHandoff(ctx, "task-1", "seat-1", testNow)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.DB().Exec(`UPDATE handoffs SET state = ? WHERE id = ?`, HandoffDone, h.ID); err != nil {
			t.Fatal(err)
		}
	}
	h, err := s.OfferHandoff(ctx, "task-1", "seat-1", testNow)
	if err != nil || h.State != HandoffExhausted || h.Attempt != MaxHandoffs+1 {
		t.Fatalf("offer after %d handoffs = %+v, %v", MaxHandoffs, h, err)
	}
	if _, err := s.OfferHandoff(ctx, "task-1", "seat-1", testNow); !errors.Is(err, ErrHandoffOpen) {
		t.Fatalf("an exhausted task gets no more offers, got %v", err)
	}
}
