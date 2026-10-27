package service

import "time"

var ustcSpeedIntervals = []struct {
	name    string
	seconds int
}{
	{"1m", 60},
	{"5m", 300},
	{"15m", 900},
	{"30m", 1800},
	{"1h", 3600},
	{"6h", 21600},
	{"1d", 86400},
}

func USTCSpeedIntervalSeconds(interval string) (int, bool) {
	for _, candidate := range ustcSpeedIntervals {
		if candidate.name == interval {
			return candidate.seconds, true
		}
	}
	return 0, false
}

var ustcSpeedHourlyBucketWidths = map[int]struct{}{
	15: {}, 30: {}, 60: {}, 120: {}, 240: {},
}

// USTCSpeedHourlyBucketMinutes reports whether minutes is a supported width for
// the folded time-of-day buckets. Every width divides a 24-hour day evenly, so
// the folded chart always ends exactly at local midnight.
func USTCSpeedHourlyBucketMinutes(minutes int) (int, bool) {
	_, ok := ustcSpeedHourlyBucketWidths[minutes]
	return minutes, ok
}

// USTCSpeedAutoInterval keeps typical windows within roughly 200 time buckets:
// 24 hours -> 15 minutes, 7 days -> 1 hour, 30 days -> 6 hours. Daily buckets
// remain useful for longer usage-history ranges. Sparse samples do not change
// the resolution, so filtering an account or a quiet period cannot silently
// smooth away the few observations available there.
func USTCSpeedAutoInterval(duration time.Duration) string {
	for _, candidate := range ustcSpeedIntervals {
		if duration <= time.Duration(candidate.seconds)*time.Second*200 {
			return candidate.name
		}
	}
	return "1d"
}
