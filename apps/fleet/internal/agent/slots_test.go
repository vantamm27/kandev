package agent

import (
	"context"
	"os"
	"os/exec"
	"testing"
)

func TestFreeSlots(t *testing.T) {
	for _, tc := range []struct{ capacity, running, want int }{{2, 0, 2}, {2, 1, 1}, {2, 2, 0}, {2, 5, 0}} {
		if got := FreeSlots(tc.capacity, tc.running); got != tc.want {
			t.Fatalf("FreeSlots(%d, %d) = %d, want %d", tc.capacity, tc.running, got, tc.want)
		}
	}
}

func TestProcessSlotCounterCountsNamedProcesses(t *testing.T) {
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skip("pgrep not installed")
	}
	counter := ProcessSlotCounter{UID: os.Getuid(), Name: "fleet-no-such-process"}
	if n, err := counter.Running(context.Background()); err != nil || n != 0 {
		t.Fatalf("no match = %d, %v", n, err)
	}
	sleeper := exec.Command("sleep", "30")
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sleeper.Process.Kill(); _ = sleeper.Wait() })
	n, err := ProcessSlotCounter{UID: os.Getuid(), Name: "sleep"}.Running(context.Background())
	if err != nil || n < 1 {
		t.Fatalf("running sleep processes = %d, %v; want at least 1", n, err)
	}
}
