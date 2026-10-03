package main

import (
	"testing"
	"time"
)

func dayAt(hour int) time.Time {
	return time.Date(2026, 10, 3, hour, 30, 0, 0, time.Local)
}

func TestCheckinDue(t *testing.T) {
	tests := []struct {
		name    string
		auto    bool
		hour    int
		lastDay string
		now     time.Time
		want    bool
	}{
		{name: "disabled", auto: false, hour: 9, now: dayAt(9), want: false},
		{name: "before the hour", auto: true, hour: 9, now: dayAt(8), want: false},
		{name: "at the hour", auto: true, hour: 9, now: dayAt(9), want: true},
		{name: "after the hour catches up", auto: true, hour: 9, now: dayAt(14), want: true},
		{name: "after the hour but already run", auto: true, hour: 9, lastDay: "2026-10-03", now: dayAt(14), want: false},
		{name: "ran yesterday runs again", auto: true, hour: 9, lastDay: "2026-10-02", now: dayAt(9), want: true},
		{name: "ran yesterday but before hour", auto: true, hour: 9, lastDay: "2026-10-02", now: dayAt(7), want: false},
		{name: "midnight hour", auto: true, hour: 0, now: dayAt(0), want: true},
		{name: "negative hour never blocks", auto: true, hour: -1, now: dayAt(0), want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := checkinDue(tc.auto, tc.hour, tc.lastDay, tc.now)
			if got != tc.want {
				t.Fatalf("checkinDue(auto=%v hour=%d lastDay=%q now=%s) = %v, want %v",
					tc.auto, tc.hour, tc.lastDay, tc.now.Format("15:04"), got, tc.want)
			}
		})
	}
}
