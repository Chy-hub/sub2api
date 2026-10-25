package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type ustcSpeedFallbackRepo struct {
	USTCSpeedRepository
	probe, real *USTCSpeedOverview
	tasks       []USTCSpeedTask
	queries     []USTCSpeedQuery
}

func (r *ustcSpeedFallbackRepo) Overview(_ context.Context, q USTCSpeedQuery) (*USTCSpeedOverview, error) {
	r.queries = append(r.queries, q)
	var value *USTCSpeedOverview
	if q.Source == "real" {
		value = r.real
	} else {
		value = r.probe
	}
	if value == nil {
		return &USTCSpeedOverview{}, nil
	}
	copy := *value
	return &copy, nil
}
func (r *ustcSpeedFallbackRepo) ListTasks(context.Context) ([]USTCSpeedTask, error) {
	return r.tasks, nil
}
func TestUSTCSpeedFallbackKeepsRealSourceAndFreshness(t *testing.T) {
	now := time.Now()
	value := 45.0
	old := now.Add(-time.Hour)
	policy := DefaultUSTCSpeedTask()
	policy.ID = 7
	policy.AccountIDs = []int64{3}
	policy.Models = []string{"model-a"}
	policy.RealFallbackMaxAgeSeconds = 600
	policy.RealFallbackMinSamples = 2
	repo := &ustcSpeedFallbackRepo{probe: &USTCSpeedOverview{Summary: USTCSpeedSummary{Skipped: 1, LatestProbeAt: &old}}, real: &USTCSpeedOverview{Summary: USTCSpeedSummary{RealSamples: 3}, Series: []USTCSpeedPoint{{Source: "real", Value: &value, ValidCount: 2}}}, tasks: []USTCSpeedTask{policy}}
	svc := NewUSTCSpeedService(repo, nil, nil)
	query := USTCSpeedQuery{Start: now.Add(-24 * time.Hour), End: now.Add(time.Hour), Source: "probe", TaskID: 7, Profile: "probe:fixed", Model: "model-a", Metric: "generation_tps"}
	result, err := svc.Overview(context.Background(), query)
	require.NoError(t, err)
	require.True(t, result.FallbackApplied)
	require.Equal(t, "probe", result.RequestedSource)
	require.Equal(t, "real", result.Series[0].Source)
	require.Equal(t, 600, result.FallbackWindowSeconds)
	require.Equal(t, query.Start, result.RangeStart, "fallback metadata retains the user's selected range")
	require.Equal(t, query.End, result.RangeEnd, "fallback metadata retains the user's selected range")
	require.Len(t, repo.queries, 2)
	realQuery := repo.queries[1]
	require.Equal(t, "real", realQuery.Source)
	require.Zero(t, realQuery.TaskID)
	require.Empty(t, realQuery.Profile)
	require.Equal(t, []int64{3}, realQuery.AccountIDs)
	require.WithinDuration(t, now.Add(-600*time.Second), realQuery.Start, time.Second)
	require.Equal(t, "probe:fixed", query.Profile, "fallback must not mutate the caller's fixed benchmark query")
}
func TestUSTCSpeedFallbackNeverSubstitutesHistoricalOrUnusableData(t *testing.T) {
	now := time.Now()
	policy := DefaultUSTCSpeedTask()
	policy.Models = []string{"m"}
	policy.ID = 1
	value := 1.0
	cases := []struct {
		name       string
		end        time.Time
		fresh      bool
		enabled    bool
		realCount  int64
		validCount int64
		minimum    int
		realValue  *float64
	}{
		{"historical", now.Add(-time.Hour), false, true, 3, 3, 1, &value},
		{"fresh benchmark", now.Add(time.Hour), true, true, 3, 3, 1, &value},
		{"policy disabled", now.Add(time.Hour), false, false, 3, 3, 1, &value},
		{"no measurable real values", now.Add(time.Hour), false, true, 3, 0, 1, nil},
		{"insufficient fresh real data", now.Add(time.Hour), false, true, 0, 0, 1, &value},
		{"failed requests cannot satisfy minimum", now.Add(time.Hour), false, true, 5, 1, 5, &value},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := policy
			task.RealFallback = tc.enabled
			task.RealFallbackMinSamples = tc.minimum
			probe := &USTCSpeedOverview{Summary: USTCSpeedSummary{Skipped: 1}}
			if tc.fresh {
				probe.Summary.LatestProbeAt = &now
			}
			repo := &ustcSpeedFallbackRepo{probe: probe, real: &USTCSpeedOverview{Summary: USTCSpeedSummary{RealSamples: tc.realCount}, Series: []USTCSpeedPoint{{Source: "real", Value: tc.realValue, ValidCount: tc.validCount}}}, tasks: []USTCSpeedTask{task}}
			svc := NewUSTCSpeedService(repo, nil, nil)
			query := USTCSpeedQuery{Start: now.Add(-24 * time.Hour), End: tc.end, Source: "probe", Model: "m", Metric: "generation_tps"}
			out, err := svc.Overview(context.Background(), query)
			require.NoError(t, err)
			require.False(t, out.FallbackApplied)
			require.Equal(t, query.Start, out.RangeStart)
			require.Equal(t, query.End, out.RangeEnd)
		})
	}
}
