package sweeper

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

type fakeMarker struct {
	cutoffs chan time.Time
	err     error
}

func (f *fakeMarker) MarkStaleSeatsOffline(_ context.Context, cutoff, _ time.Time) (int64, error) {
	f.cutoffs <- cutoff
	return 1, f.err
}

func TestSweepUsesTimeoutCutoff(t *testing.T) {
	marker := &fakeMarker{cutoffs: make(chan time.Time, 1)}
	s := New(marker, 90*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	s.Sweep(context.Background())
	if got := <-marker.cutoffs; !got.Equal(now.Add(-90 * time.Second)) {
		t.Fatalf("cutoff = %s", got)
	}
	marker.err = errors.New("db locked")
	s.Sweep(context.Background()) // logs and returns; must not panic
	<-marker.cutoffs
}

func TestRunSweepsUntilCancelled(t *testing.T) {
	marker := &fakeMarker{cutoffs: make(chan time.Time, 16)}
	s := New(marker, 3*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Run(ctx)
	}()
	select {
	case <-marker.cutoffs:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not sweep within one interval")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}
