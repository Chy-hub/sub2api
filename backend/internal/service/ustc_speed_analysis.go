package service

import (
	"time"
)

// These are query results only. Analysis adds no fields to usage collection or
// persistence, and every statistic is computed from individual valid calls.
type USTCSpeedAnalysisStats struct {
	Period     string   `json:"period"`
	Model      string   `json:"model"`
	Source     string   `json:"source"`
	Count      int64    `json:"count"`
	ValidCount int64    `json:"valid_count"`
	Mean       *float64 `json:"mean"`
	P10        *float64 `json:"p10"`
	P50        *float64 `json:"p50"`
	P90        *float64 `json:"p90"`
	Stddev     *float64 `json:"stddev"`
	Min        *float64 `json:"min"`
	Max        *float64 `json:"max"`
}

type USTCSpeedAnalysisLength struct {
	USTCSpeedAnalysisStats
	Dimension string `json:"dimension"`
	Bucket    int    `json:"bucket"`
}

type USTCSpeedAnalysisBin struct {
	Period string  `json:"period"`
	Model  string  `json:"model"`
	Source string  `json:"source"`
	Bin    int     `json:"bin"`
	Lower  float64 `json:"lower"`
	Upper  float64 `json:"upper"`
	Count  int64   `json:"count"`
}

type USTCSpeedAnalysisTrend struct {
	USTCSpeedAnalysisStats
	Bucket int `json:"bucket"`
}

type USTCSpeedAnalysis struct {
	CurrentStart  time.Time                 `json:"current_start"`
	CurrentEnd    time.Time                 `json:"current_end"`
	PreviousStart *time.Time                `json:"previous_start"`
	PreviousEnd   *time.Time                `json:"previous_end"`
	Comparison    string                    `json:"comparison"`
	BucketSeconds int                       `json:"bucket_seconds"`
	Groups        []USTCSpeedAnalysisStats  `json:"groups"`
	Distribution  []USTCSpeedAnalysisBin    `json:"distribution"`
	Lengths       []USTCSpeedAnalysisLength `json:"lengths"`
	Trend         []USTCSpeedAnalysisTrend  `json:"trend"`
}

// USTCSpeedAnalysisWindow compares elapsed portions of calendar periods. Today
// at noon must not be compared with a complete previous day. Calendar shifts
// follow the selected timezone, and month/year shifts clamp month-end dates.
func USTCSpeedAnalysisWindow(q USTCSpeedQuery, now time.Time) USTCSpeedAnalysis {
	zone, err := time.LoadLocation(q.Timezone)
	if err != nil {
		zone = time.UTC
	}
	start, end := q.Start.In(zone), q.End.In(zone)
	if end.After(now) && start.Before(now) {
		end = now.In(zone)
	}
	out := USTCSpeedAnalysis{CurrentStart: start, CurrentEnd: end, Comparison: q.Comparison}
	if out.Comparison == "" {
		out.Comparison = "previous"
	}
	// At most 120 comparison buckets, with a one-minute minimum. Buckets are
	// aligned by elapsed time since each period's start, not by unrelated dates.
	out.BucketSeconds = max(60, int((end.Sub(start).Seconds()+119)/120))
	if out.Comparison == "none" || !end.After(start) {
		return out
	}
	var previousStart, previousEnd time.Time
	switch out.Comparison {
	case "week":
		previousStart, previousEnd = start.AddDate(0, 0, -7), end.AddDate(0, 0, -7)
	case "month":
		previousStart, previousEnd = ustcSpeedShiftMonth(start, -1), ustcSpeedShiftMonth(end, -1)
	case "year":
		previousStart, previousEnd = ustcSpeedShiftMonth(start, -12), ustcSpeedShiftMonth(end, -12)
	default:
		out.Comparison = "previous"
		if q.LastHours > 0 {
			previousStart = start.Add(-q.End.Sub(q.Start))
			previousEnd = previousStart.Add(end.Sub(start))
		} else {
			// Date ranges are calendar-day based; avoid a 23/25-hour DST day
			// shifting the beginning away from local midnight.
			a := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
			b := time.Date(q.End.In(zone).Year(), q.End.In(zone).Month(), q.End.In(zone).Day(), 0, 0, 0, 0, time.UTC)
			days := max(1, int(b.Sub(a)/(24*time.Hour)))
			previousStart, previousEnd = start.AddDate(0, 0, -days), end.AddDate(0, 0, -days)
		}
	}
	if out.Comparison == "week" || out.Comparison == "month" || out.Comparison == "year" {
		if q.LastHours > 0 {
			previousEnd = previousStart.Add(end.Sub(start))
		} else {
			// Shift the start date, then preserve the current window's calendar
			// length. Shifting both ends independently could collapse March
			// 30–31 into February 28–28 (or February 28–29 in a leap year).
			a := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
			b := time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, time.UTC)
			day := previousStart.AddDate(0, 0, int(b.Sub(a)/(24*time.Hour)))
			previousEnd = time.Date(day.Year(), day.Month(), day.Day(), end.Hour(), end.Minute(), end.Second(), end.Nanosecond(), zone)
		}
	}
	out.PreviousStart, out.PreviousEnd = &previousStart, &previousEnd
	return out
}

func ustcSpeedShiftMonth(t time.Time, months int) time.Time {
	first := time.Date(t.Year(), t.Month()+time.Month(months), 1, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())
	lastDay := time.Date(first.Year(), first.Month()+1, 0, 0, 0, 0, 0, t.Location()).Day()
	return time.Date(first.Year(), first.Month(), min(t.Day(), lastDay), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())
}
