package handoff

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kandev/kandev/apps/fleet/internal/kandev"
	"github.com/kandev/kandev/apps/fleet/internal/store"
)

var now = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

type fakeKandev struct {
	assignee  string
	assigned  []string
	assignErr error
}

func (f *fakeKandev) GetTask(_ context.Context, id string) (kandev.Task, error) {
	return kandev.Task{ID: id, AssigneeUserID: f.assignee, Priority: "high"}, nil
}

func (f *fakeKandev) SetTaskAssignee(_ context.Context, _ string, userID string) (kandev.Task, error) {
	f.assigned = append(f.assigned, userID)
	if f.assignErr != nil {
		return kandev.Task{}, f.assignErr
	}
	f.assignee = userID
	return kandev.Task{AssigneeUserID: userID}, nil
}

type fakeLauncher struct {
	calls []kandev.LaunchSessionInput
	err   error
}

func (f *fakeLauncher) LaunchSession(_ context.Context, in kandev.LaunchSessionInput) (kandev.LaunchSessionResult, error) {
	f.calls = append(f.calls, in)
	if f.err != nil {
		return kandev.LaunchSessionResult{}, f.err
	}
	return kandev.LaunchSessionResult{Success: true, SessionID: "sess-new"}, nil
}

func setup(t *testing.T) (*store.Store, *fakeKandev, *fakeLauncher, *Service, store.Handoff) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "fleet.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	for _, s := range []struct{ id, owner, host string }{{"seat_a", "user-a", "mini-1"}, {"seat_b", "user-b", "mini-2"}, {"seat_a2", "user-a", "mini-3"}} {
		if err := st.CreateToken(ctx, "tok-"+s.id, store.RegistrationToken{OwnerUserID: s.owner, ExpiresAt: now.Add(time.Hour)}, now); err != nil {
			t.Fatal(err)
		}
		if err := st.RegisterSeat(ctx, "tok-"+s.id, store.Seat{ID: s.id, OwnerUserID: s.owner, SSHHost: s.host, SSHPort: 22, SSHUser: "seat",
			HostKeyFingerprint: "SHA256:x", Capacity: 1, KandevExecutorProfileID: "prof-" + s.id, HMACSecret: "s", State: store.SeatOnline}, now); err != nil {
			t.Fatal(err)
		}
		if err := st.ApplyHeartbeat(ctx, s.id, store.HeartbeatUpdate{State: store.SeatOnline, FreeSlots: 1, SignedAt: 1}, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.BeginClaim(ctx, "task-1", "seat_a", now); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishClaim(ctx, "task-1", store.ClaimLaunched, "sess-old", "", now); err != nil {
		t.Fatal(err)
	}
	h, err := st.OfferHandoff(ctx, "task-1", "seat_a", now)
	if err != nil {
		t.Fatal(err)
	}
	k := &fakeKandev{assignee: "user-a"}
	l := &fakeLauncher{}
	svc := New(st, k, l, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.now = func() time.Time { return now }
	return st, k, l, svc, h
}

func TestAcceptMovesTaskToMemberSeat(t *testing.T) {
	st, k, l, svc, h := setup(t)
	done, err := svc.Accept(context.Background(), h.ID, "seat_b")
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if done.State != store.HandoffDone || done.Path != PathResume || done.ToSeatID != "seat_b" {
		t.Fatalf("handoff = %+v", done)
	}
	if k.assignee != "user-b" {
		t.Fatalf("assignee = %q, want the accepting member", k.assignee)
	}
	if len(l.calls) != 1 || l.calls[0].ExecutorProfileID != "prof-seat_b" || l.calls[0].Intent != kandev.IntentStart ||
		!strings.Contains(l.calls[0].Prompt, "refs/agent-wip/task-1") || l.calls[0].Priority != "high" {
		t.Fatalf("launch = %+v", l.calls)
	}
	claims, _ := st.ListClaims(context.Background())
	if c := claims["task-1"]; c.SeatID != "seat_b" || c.SessionID != "sess-new" || c.State != store.ClaimLaunched {
		t.Fatalf("claim = %+v", c)
	}
	if _, err := svc.Accept(context.Background(), h.ID, "seat_b"); !errors.Is(err, store.ErrSeatStateConflict) {
		t.Fatalf("accepting twice = %v", err)
	}
}

func TestAcceptFailureRestoresAssignee(t *testing.T) {
	st, k, l, svc, h := setup(t)
	l.err = errors.New("kandev: session cannot start on another executor")
	_, err := svc.Accept(context.Background(), h.ID, "seat_b")
	var kerr *KandevError
	if !errors.As(err, &kerr) {
		t.Fatalf("err = %v, want KandevError", err)
	}
	if strings.Join(k.assigned, ",") != "user-b,user-a" || k.assignee != "user-a" {
		t.Fatalf("assignee changes = %v; a failed move must restore the original owner", k.assigned)
	}
	got, _ := st.GetHandoff(context.Background(), h.ID)
	if got.State != store.HandoffFailed {
		t.Fatalf("handoff state = %s", got.State)
	}
	if _, err := st.OfferHandoff(context.Background(), "task-1", "seat_a", now); err != nil {
		t.Fatalf("a failed handoff can be offered again: %v", err)
	}
	claims, _ := st.ListClaims(context.Background())
	if claims["task-1"].SeatID != "seat_a" {
		t.Fatal("the claim stays on the original seat after a failed move")
	}
}

func TestAcceptRejections(t *testing.T) {
	st, _, l, svc, h := setup(t)
	ctx := context.Background()
	if _, err := svc.Accept(ctx, h.ID, "seat_a2"); !errors.Is(err, ErrSameOwner) {
		t.Fatalf("same owner = %v", err)
	}
	if err := st.ApplyHeartbeat(ctx, "seat_b", store.HeartbeatUpdate{State: store.SeatBusy, FreeSlots: 0, SignedAt: 2}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Accept(ctx, h.ID, "seat_b"); !errors.Is(err, ErrSeatUnavailable) {
		t.Fatalf("busy seat = %v", err)
	}
	if _, err := svc.Accept(ctx, h.ID, "seat_missing"); !errors.Is(err, store.ErrSeatNotFound) {
		t.Fatalf("missing seat = %v", err)
	}
	if _, err := svc.Accept(ctx, "hoff_missing", "seat_b"); !errors.Is(err, store.ErrHandoffNotFound) {
		t.Fatalf("missing handoff = %v", err)
	}
	if len(l.calls) != 0 {
		t.Fatal("rejected accepts must not launch anything")
	}
	if got, _ := st.GetHandoff(ctx, h.ID); got.State != store.HandoffOffered {
		t.Fatalf("a rejected accept leaves the offer open, state = %s", got.State)
	}
}
