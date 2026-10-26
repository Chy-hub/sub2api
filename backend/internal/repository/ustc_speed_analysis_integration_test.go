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

func TestUSTCSpeedAnalysisUsesCallsAndCompleteHistory(t *testing.T) {
	ctx := context.Background()
	repo := NewUSTCSpeedRepository(integrationDB)
	model := "analysis-" + uuid.NewString()
	base := time.Date(2025, 10, 6, 0, 0, 0, 0, time.UTC)
	account := -time.Now().UnixNano()
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(context.Background(), `DELETE FROM ustc_speed_samples WHERE model=$1`, model)
		require.NoError(t, err)
	})
	for period := 0; period < 2; period++ {
		for i := 0; i < 20; i++ {
			at := base.Add(time.Duration(i+1) * time.Second)
			value := float64(10)
			if i >= 18 {
				value = 100
			}
			if period == 1 {
				at = at.Add(-24 * time.Hour)
				value = 20
			}
			id := account
			if i >= 18 {
				id--
			}
			sample := &service.USTCSpeedSample{AccountID: id, Model: model, Source: "probe", Status: "success", StartedAt: at, FinishedAt: at,
				USTCSpeedObservation: service.USTCSpeedObservation{GenerationTPS: &value, InputTokensKnown: true, InputTokens: 100, TokenCountKnown: true, OutputTokens: 200}}
			require.NoError(t, repo.InsertSample(ctx, sample))
		}
	}
	failValue := float64(999)
	require.NoError(t, repo.InsertSample(ctx, &service.USTCSpeedSample{AccountID: account, Model: model, Source: "probe", Status: "error", StartedAt: base.Add(time.Hour), FinishedAt: base.Add(time.Hour), USTCSpeedObservation: service.USTCSpeedObservation{GenerationTPS: &failValue}}))
	q := service.USTCSpeedQuery{Start: base, End: base.Add(24 * time.Hour), Model: model, Source: "probe", Metric: "generation_tps", Interval: "raw", Timezone: "UTC", Analysis: true, Comparison: "previous"}
	out, err := repo.Overview(ctx, q)
	require.NoError(t, err)
	require.NotNil(t, out.Analysis)
	require.Len(t, out.Analysis.Groups, 2)
	for _, row := range out.Analysis.Groups {
		require.EqualValues(t, 20, row.ValidCount)
		if row.Period == "current" {
			require.EqualValues(t, 21, row.Count)
			require.InDelta(t, 19, *row.Mean, 1e-8, "18 calls on one account and 2 on another are pooled by calls")
			require.InDelta(t, 10, *row.P50, 1e-8)
		} else {
			require.InDelta(t, 20, *row.Mean, 1e-8)
		}
	}
	counts := map[string]int64{}
	for _, bin := range out.Analysis.Distribution {
		counts[bin.Period] += bin.Count
	}
	require.EqualValues(t, 20, counts["current"])
	require.EqualValues(t, 20, counts["previous"])
	// Detail/chart limits must never become limits on distribution or comparisons.
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO ustc_speed_samples(account_id,model,source,status,profile,reasoning,stream,started_at,finished_at,payload)
 SELECT $1,$2,'probe','success','','off',true,$3,$3,'{"generation_tps":30}'::jsonb FROM generate_series(1,5001)`, account, model, base.Add(2*time.Hour))
	require.NoError(t, err)
	out, err = repo.Overview(ctx, q)
	require.NoError(t, err)
	require.True(t, out.Truncated)
	require.Len(t, out.Series, 5000)
	for _, row := range out.Analysis.Groups {
		if row.Period == "current" {
			require.EqualValues(t, 5021, row.ValidCount)
			require.InDelta(t, (19.0*20+30*5001)/5021, *row.Mean, 1e-8)
		}
	}
	q.AccountIDs = []int64{account - 1}
	out, err = repo.Overview(ctx, q)
	require.NoError(t, err)
	for _, row := range out.Analysis.Groups {
		if row.Period == "current" {
			require.EqualValues(t, 2, row.ValidCount)
			require.InDelta(t, 100, *row.Mean, 1e-8, "account selection is a filter on calls")
		}
	}
}
