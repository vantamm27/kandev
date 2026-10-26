package seatstate

import (
	"testing"

	"github.com/kandev/kandev/apps/fleet/internal/store"
)

func TestNext(t *testing.T) {
	for name, tc := range map[string]struct {
		current string
		hb      Heartbeat
		want    string
	}{
		"free slot":               {store.SeatOnline, Heartbeat{FreeSlots: 1}, store.SeatOnline},
		"no free slot":            {store.SeatOnline, Heartbeat{FreeSlots: 0}, store.SeatBusy},
		"5h limit":                {store.SeatBusy, Heartbeat{Limit: Limit5h, FreeSlots: 1}, store.SeatCooldown5h},
		"weekly beats 5h":         {store.SeatCooldown5h, Heartbeat{Limit: LimitWeek}, store.SeatCooldownWeek},
		"limit cleared":           {store.SeatCooldown5h, Heartbeat{FreeSlots: 2}, store.SeatOnline},
		"offline comes back":      {store.SeatOffline, Heartbeat{FreeSlots: 1}, store.SeatOnline},
		"registering goes online": {store.SeatRegistering, Heartbeat{FreeSlots: 1}, store.SeatOnline},
		"draining is kept":        {store.SeatDraining, Heartbeat{FreeSlots: 1}, store.SeatDraining},
		"draining keeps on limit": {store.SeatDraining, Heartbeat{Limit: LimitWeek}, store.SeatDraining},
		"retired is kept":         {store.SeatRetired, Heartbeat{FreeSlots: 1}, store.SeatRetired},
	} {
		t.Run(name, func(t *testing.T) {
			if got := Next(tc.current, tc.hb); got != tc.want {
				t.Fatalf("Next(%s, %+v) = %s, want %s", tc.current, tc.hb, got, tc.want)
			}
		})
	}
}

func TestValid(t *testing.T) {
	high, low, ok := 100.5, -1.0, 42.0
	for name, tc := range map[string]struct {
		hb   Heartbeat
		want bool
	}{
		"empty":          {Heartbeat{}, true},
		"full":           {Heartbeat{Limit: Limit5h, Quota5hPct: &ok, QuotaWeekPct: &ok, FreeSlots: 2}, true},
		"unknown limit":  {Heartbeat{Limit: "daily"}, false},
		"negative slots": {Heartbeat{FreeSlots: -1}, false},
		"pct above 100":  {Heartbeat{Quota5hPct: &high}, false},
		"pct below 0":    {Heartbeat{QuotaWeekPct: &low}, false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := tc.hb.Valid(); got != tc.want {
				t.Fatalf("Valid() = %v, want %v", got, tc.want)
			}
		})
	}
}
