package model

import (
	"testing"
	"time"
)

func TestTrafficQuotaWindowUsesBeijingCalendar(t *testing.T) {
	monday := time.Date(2026, 11, 8, 16, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		period string
		now    time.Time
		want   int64
	}{
		{TrafficResetNone, monday, 0},
		{"daily", monday, monday.Unix()},
		{"weekly", monday, monday.Unix()},
		{"weekly", monday.Add(-time.Second), monday.AddDate(0, 0, -7).Unix()},
		{"monthly", monday, time.Date(2026, 10, 31, 16, 0, 0, 0, time.UTC).Unix()},
	} {
		if got := TrafficQuotaWindow(test.period, test.now); got != test.want {
			t.Errorf("%s window at %s = %d, want %d", test.period, test.now, got, test.want)
		}
	}
}
