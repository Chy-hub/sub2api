package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUSTCSpeedAutoInterval(t *testing.T) {
	for _, tc := range []struct {
		duration time.Duration
		want     string
	}{
		{time.Hour, "1m"},
		{200 * time.Minute, "1m"},
		{200*time.Minute + time.Nanosecond, "5m"},
		{12 * time.Hour, "5m"},
		{24*time.Hour + time.Nanosecond, "15m"},
		{3 * 24 * time.Hour, "30m"},
		{7 * 24 * time.Hour, "1h"},
		{30 * 24 * time.Hour, "6h"},
		{180 * 24 * time.Hour, "1d"},
		{365 * 24 * time.Hour, "1d"},
	} {
		t.Run(tc.duration.String(), func(t *testing.T) {
			interval := USTCSpeedAutoInterval(tc.duration)
			require.Equal(t, tc.want, interval)
			seconds, ok := USTCSpeedIntervalSeconds(interval)
			require.True(t, ok)
			if tc.duration <= 200*24*time.Hour {
				require.LessOrEqual(t, tc.duration.Seconds()/float64(seconds), float64(200))
			}
		})
	}
}

func TestUSTCSpeedAnalysisUsesReadableAutoInterval(t *testing.T) {
	start := time.Date(2026, 10, 25, 13, 24, 0, 0, time.UTC)
	q := USTCSpeedQuery{Start: start, End: start.Add(24*time.Hour + time.Nanosecond), Timezone: "UTC", LastHours: 24}
	out := USTCSpeedAnalysisWindow(q, q.End)
	require.Equal(t, 900, out.BucketSeconds)
	require.Equal(t, q.Start.Add(-q.End.Sub(q.Start)), *out.PreviousStart)
	require.Equal(t, q.Start, *out.PreviousEnd)
}
