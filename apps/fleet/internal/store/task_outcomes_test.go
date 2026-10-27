package store

import (
	"context"
	"testing"
	"time"
)

func TestRecordTaskStateKeepsTheFirstFinish(t *testing.T) {
	s := registeredStore(t, newSeat("seat-1", "mini-1", "seat-a"))
	ctx := context.Background()
	for _, id := range []string{"task-run", "task-fail"} {
		if _, err := s.BeginClaim(ctx, id, "seat-1", testNow); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.FinishClaim(ctx, "task-run", ClaimLaunched, "session-1", "", testNow); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishClaim(ctx, "task-fail", ClaimFailed, "", "boom", testNow); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordTaskState(ctx, "task-run", "IN_PROGRESS", false, testNow.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	done := testNow.Add(10 * time.Minute)
	if err := s.RecordTaskState(ctx, "task-run", "REVIEW", true, done); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordTaskState(ctx, "task-run", "COMPLETED", true, done.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	outcomes, err := s.ListTaskOutcomes(ctx)
	if err != nil || len(outcomes) != 2 {
		t.Fatalf("outcomes = %+v, %v", outcomes, err)
	}
	byID := map[string]TaskOutcome{}
	for _, o := range outcomes {
		byID[o.TaskID] = o
	}
	run := byID["task-run"]
	if run.TaskState != "REVIEW" || run.FinishedAt == nil || !run.FinishedAt.Equal(done) || run.OwnerUserID == "" {
		t.Fatalf("finished task must keep its first finished state = %+v", run)
	}
	if claims, _ := s.ListClaims(ctx); claims["task-run"].TaskState != "REVIEW" || claims["task-run"].FinishedAt == nil {
		t.Fatalf("ListClaims must carry the tracked state: %+v", claims["task-run"])
	}
	if fail := byID["task-fail"]; fail.State != ClaimFailed || fail.FinishedAt != nil {
		t.Fatalf("failed claim = %+v", fail)
	}
}

func TestRetryClearsRecordedTaskState(t *testing.T) {
	s := registeredStore(t, newSeat("seat-1", "mini-1", "seat-a"))
	ctx := context.Background()
	if _, err := s.BeginClaim(ctx, "task-1", "seat-1", testNow); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishClaim(ctx, "task-1", ClaimLaunched, "s", "", testNow); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordTaskState(ctx, "task-1", "FAILED", true, testNow); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE dispatch_claims SET state = ? WHERE task_id = ?`, ClaimFailed, "task-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginClaim(ctx, "task-1", "seat-1", testNow.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	outcomes, err := s.ListTaskOutcomes(ctx)
	if err != nil || outcomes[0].TaskState != "" || outcomes[0].FinishedAt != nil {
		t.Fatalf("retried claim = %+v, %v", outcomes, err)
	}
}
