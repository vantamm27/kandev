package store

import (
	"context"
	"testing"
	"time"
)

func launchedClaim(t *testing.T, s *Store, taskID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.BeginClaim(ctx, taskID, "seat-1", testNow); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishClaim(ctx, taskID, ClaimLaunched, "session-"+taskID, "", testNow); err != nil {
		t.Fatal(err)
	}
}

func TestRecordTaskStateStampsEachChange(t *testing.T) {
	s := registeredStore(t, newSeat("seat-1", "mini-1", "seat-a"))
	ctx := context.Background()
	launchedClaim(t, s, "task-1")
	review := testNow.Add(time.Minute)
	if err := s.RecordTaskState(ctx, "task-1", "REVIEW", false, review); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordTaskState(ctx, "task-1", "REVIEW", false, review.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	claims, err := s.ListClaims(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c := claims["task-1"]
	if c.TaskState != "REVIEW" || c.TaskStateAt == nil || !c.TaskStateAt.Equal(review) || c.FinishedAt != nil {
		t.Fatalf("an unchanged state must keep the time it was first seen: %+v", c)
	}
}

func TestReleaseClaimAndRelaunch(t *testing.T) {
	s := registeredStore(t, newSeat("seat-1", "mini-1", "seat-a"))
	ctx := context.Background()
	launchedClaim(t, s, "task-1")
	if err := s.RecordTaskState(ctx, "task-1", "REVIEW", false, testNow.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	released := testNow.Add(time.Hour)
	ok, err := s.ReleaseClaim(ctx, "task-1", "step-approve", released)
	if err != nil || !ok {
		t.Fatalf("release = %v, %v", ok, err)
	}
	if ok, err := s.ReleaseClaim(ctx, "task-1", "step-approve", released); err != nil || ok {
		t.Fatalf("a released claim must not be released again: %v, %v", ok, err)
	}
	outcomes, err := s.ListTaskOutcomes(ctx)
	if err != nil || len(outcomes) != 1 {
		t.Fatalf("outcomes = %+v, %v", outcomes, err)
	}
	o := outcomes[0]
	if o.State != ClaimReleased || o.FinishedAt == nil || !o.FinishedAt.Equal(released) || o.ReleasedStepID != "step-approve" || o.Outcome() != "released" {
		t.Fatalf("released claim = %+v (outcome %q)", o, o.Outcome())
	}
	attempt, err := s.BeginClaim(ctx, "task-1", "seat-1", released.Add(time.Minute))
	if err != nil || attempt != 1 {
		t.Fatalf("relaunching a released task = %d, %v; want a fresh attempt budget", attempt, err)
	}
	claims, _ := s.ListClaims(ctx)
	if c := claims["task-1"]; c.State != ClaimLaunching || c.FinishedAt != nil || c.TaskState != "" || c.TaskStateAt != nil || c.ReleasedStepID != "" {
		t.Fatalf("relaunch must start a clean claim: %+v", c)
	}
}

func TestAdoptSessionResumesTracking(t *testing.T) {
	s := registeredStore(t, newSeat("seat-1", "mini-1", "seat-a"))
	ctx := context.Background()
	launchedClaim(t, s, "task-1")
	if _, err := s.ReleaseClaim(ctx, "task-1", "step-approve", testNow.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	adopted := testNow.Add(2 * time.Hour)
	if err := s.AdoptSession(ctx, "task-1", "session-2", adopted); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	claims, _ := s.ListClaims(ctx)
	c := claims["task-1"]
	if c.State != ClaimLaunched || c.SessionID != "session-2" || c.FinishedAt != nil || c.TaskStateAt != nil || c.ReleasedStepID != "" || !c.UpdatedAt.Equal(adopted) {
		t.Fatalf("adopted claim = %+v", c)
	}
	if err := s.AdoptSession(ctx, "task-1", "session-3", adopted); err == nil {
		t.Fatal("only a released claim can adopt a session")
	}
}
