package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUSTCSpeedAnalysisWindow(t *testing.T) {
	zone, err := time.LoadLocation("Asia/Shanghai")
	require.NoError(t, err)
	parse := func(value string) time.Time {
		at, err := time.ParseInLocation("2006-01-02 15:04", value, zone)
		require.NoError(t, err)
		return at
	}
	q := USTCSpeedQuery{Start: parse("2026-10-26 00:00"), End: parse("2026-10-27 00:00"), Timezone: "Asia/Shanghai"}
	now := parse("2026-10-26 12:30")
	out := USTCSpeedAnalysisWindow(q, now)
	require.Equal(t, now, out.CurrentEnd)
	require.True(t, out.PreviousStart.Equal(parse("2026-10-25 00:00")))
	require.True(t, out.PreviousEnd.Equal(parse("2026-10-25 12:30")), "a partial day is not compared with a complete day")
	q.Comparison = "week"
	out = USTCSpeedAnalysisWindow(q, now)
	require.True(t, out.PreviousStart.Equal(parse("2026-10-19 00:00")))
	require.True(t, out.PreviousEnd.Equal(parse("2026-10-19 12:30")))
	q.Comparison = "none"
	require.Nil(t, USTCSpeedAnalysisWindow(q, now).PreviousStart)
	q.Start, q.End, q.Comparison = now.Add(-24*time.Hour), now, "previous"
	q.LastHours = 24
	out = USTCSpeedAnalysisWindow(q, now)
	require.Equal(t, q.Start.Add(-24*time.Hour), *out.PreviousStart)
	require.Equal(t, q.Start, *out.PreviousEnd)
}

func TestUSTCSpeedAnalysisCalendarShiftAndDST(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	start := time.Date(2026, 3, 8, 0, 0, 0, 0, ny)
	end := start.AddDate(0, 0, 1)
	out := USTCSpeedAnalysisWindow(USTCSpeedQuery{Start: start, End: end, Timezone: ny.String()}, end.Add(time.Hour))
	require.Equal(t, 23*time.Hour, end.Sub(start))
	require.Equal(t, 0, out.PreviousStart.Hour())
	require.Equal(t, 24*time.Hour, out.PreviousEnd.Sub(*out.PreviousStart))
	for _, tc := range []struct {
		at     string
		months int
		want   string
	}{
		{"2026-03-31", -1, "2026-02-28"},
		{"2024-02-29", -12, "2023-02-28"},
		{"2026-01-31", -1, "2025-12-31"},
	} {
		at, err := time.Parse("2006-01-02", tc.at)
		require.NoError(t, err)
		require.Equal(t, tc.want, ustcSpeedShiftMonth(at, tc.months).Format("2006-01-02"))
	}
	monthStart := time.Date(2026, 3, 30, 0, 0, 0, 0, time.UTC)
	out = USTCSpeedAnalysisWindow(USTCSpeedQuery{Start: monthStart, End: monthStart.AddDate(0, 0, 1), Timezone: "UTC", Comparison: "month"}, monthStart.AddDate(0, 1, 0))
	require.Equal(t, "2026-02-28", out.PreviousStart.Format("2006-01-02"))
	require.Equal(t, "2026-03-01", out.PreviousEnd.Format("2006-01-02"))
}
