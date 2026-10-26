//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestUSTCSpeedStatsUseActualRequestsAndKnownMetadata(t *testing.T) {
	ctx := context.Background()
	repo := NewUSTCSpeedRepository(integrationDB)
	model := "stats-" + uuid.NewString()
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	account := -time.Now().UnixNano()
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(context.Background(), `DELETE FROM ustc_speed_samples WHERE model=$1`, model)
		require.NoError(t, err)
	})
	rows := []struct {
		status      string
		observation service.USTCSpeedObservation
	}{
		{"success", service.USTCSpeedObservation{InputTokens: 10, InputTokensKnown: true, OutputTokens: 20, TokenCountKnown: true, CachedTokens: 7, ReasoningTokens: 9, ProviderCost: ustcSpeedFloat(0.1), TotalMS: ustcSpeedFloat(1000)}},
		{"error", service.USTCSpeedObservation{InputTokens: 5, InputTokensKnown: true, TokenCountKnown: true, ProviderCost: ustcSpeedFloat(0), TotalMS: ustcSpeedFloat(2000)}},
		{"cancelled", service.USTCSpeedObservation{TotalMS: ustcSpeedFloat(1000)}},
		// Even malformed old skipped samples must never inflate request cards.
		{"skipped", service.USTCSpeedObservation{InputTokens: 999, InputTokensKnown: true, OutputTokens: 999, TokenCountKnown: true, ProviderCost: ustcSpeedFloat(999), TotalMS: ustcSpeedFloat(0)}},
	}
	for i, row := range rows {
		require.NoError(t, repo.InsertSample(ctx, &service.USTCSpeedSample{AccountID: account - int64(i), Model: model, Source: "probe", Status: row.status, StartedAt: base.Add(time.Duration(i) * time.Minute), FinishedAt: base.Add(time.Hour), USTCSpeedObservation: row.observation}))
	}
	// The selected period excludes this otherwise matching request.
	require.NoError(t, repo.InsertSample(ctx, &service.USTCSpeedSample{AccountID: account, Model: model, Source: "probe", Status: "success", StartedAt: base.Add(-time.Hour), FinishedAt: base, USTCSpeedObservation: rows[0].observation}))
	q := service.USTCSpeedQuery{Start: base, End: base.Add(time.Hour), Model: model, Source: "probe", Metric: "overall_tps", Interval: "auto"}
	out, err := repo.Overview(ctx, q)
	require.NoError(t, err)
	require.EqualValues(t, 4, out.Summary.Samples)
	s := out.Stats
	require.NotNil(t, s)
	require.EqualValues(t, 3, s.TotalRequests)
	require.EqualValues(t, 15, *s.TotalInputTokens)
	require.EqualValues(t, 20, *s.TotalOutputTokens)
	require.EqualValues(t, 35, *s.TotalTokens, "cached/reasoning tokens are already subsets and must not be added twice")
	require.InDelta(t, 0.1, *s.TotalProviderCost, 1e-9)
	require.InDelta(t, 4000.0/3, *s.AverageDurationMS, 1e-9)
	require.EqualValues(t, 2, s.InputSamples)
	require.EqualValues(t, 2, s.OutputSamples)
	require.EqualValues(t, 2, s.CostSamples, "a reported zero is known cost")
	require.EqualValues(t, 3, s.DurationSamples)
	q.AccountIDs = []int64{account - 2}
	out, err = repo.Overview(ctx, q)
	require.NoError(t, err)
	require.EqualValues(t, 1, out.Stats.TotalRequests)
	require.Nil(t, out.Stats.TotalTokens)
	require.Nil(t, out.Stats.TotalProviderCost, "missing cost is not zero")
	require.EqualValues(t, 0, out.Stats.CostSamples)
	q.Status, q.AccountIDs = "error", nil
	out, err = repo.Overview(ctx, q)
	require.NoError(t, err)
	require.EqualValues(t, 1, out.Stats.TotalRequests)
	require.Equal(t, float64(0), *out.Stats.TotalProviderCost)
	require.EqualValues(t, 5, *out.Stats.TotalTokens)
	q.Status = "skipped"
	out, err = repo.Overview(ctx, q)
	require.NoError(t, err)
	require.Zero(t, out.Stats.TotalRequests)
	require.Zero(t, *out.Stats.TotalTokens)
	require.Zero(t, *out.Stats.TotalProviderCost)
	require.Nil(t, out.Stats.AverageDurationMS)
}
