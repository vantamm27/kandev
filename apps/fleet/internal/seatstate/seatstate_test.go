package seatstate

import (
	"testing"
)

func TestNext(t *testing.T) {
	for name, tc := range map[string]struct {
		current string
		hb      Heartbeat
		want    string
	}{
		"free slot":               {Online, Heartbeat{FreeSlots: 1}, Online},
		"no free slot":            {Online, Heartbeat{FreeSlots: 0}, Busy},
		"5h limit":                {Busy, Heartbeat{Limit: Limit5h, FreeSlots: 1}, Cooldown5h},
		"weekly beats 5h":         {Cooldown5h, Heartbeat{Limit: LimitWeek}, CooldownWeek},
		"limit cleared":           {Cooldown5h, Heartbeat{FreeSlots: 2}, Online},
		"offline comes back":      {Offline, Heartbeat{FreeSlots: 1}, Online},
		"registering goes online": {Registering, Heartbeat{FreeSlots: 1}, Online},
		"draining is kept":        {Draining, Heartbeat{FreeSlots: 1}, Draining},
		"draining keeps on limit": {Draining, Heartbeat{Limit: LimitWeek}, Draining},
		"retired is kept":         {Retired, Heartbeat{FreeSlots: 1}, Retired},
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
