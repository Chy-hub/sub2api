//go:build integration

package repository

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestUSTCSpeedRepositorySampleCRUDAndOverview(t *testing.T) {
	ctx := context.Background()
	repo := NewUSTCSpeedRepository(integrationDB)
	accountID := -time.Now().UnixNano()
	roundID := "ustc-it-" + uuid.NewString()
	base := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(context.Background(), `DELETE FROM ustc_speed_runtime WHERE key_scope=$1`, "physical-key-"+roundID)
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(context.Background(), `DELETE FROM ustc_speed_samples WHERE round_id=$1`, roundID)
		require.NoError(t, err)
	})

	metrics := []*float64{ustcSpeedFloat(10), ustcSpeedFloat(20), ustcSpeedFloat(30), ustcSpeedFloat(999), ustcSpeedFloat(888), ustcSpeedFloat(100)}
	profiles := []string{"p-a", "p-a", "p-a", "p-a", "p-a", "p-b"}
	statuses := []string{"success", "success", "recorded", "error", "skipped", "success"}
	created := make([]*service.USTCSpeedSample, 0, len(metrics))
	for i := range metrics {
		at := base.Add(time.Duration(i) * 10 * time.Second)
		sample := &service.USTCSpeedSample{
			RoundID:     roundID,
			AccountID:   accountID,
			AccountName: "ustc-integration-account",
			Model:       "ustc-model-" + roundID,
			Source:      "probe",
			Status:      statuses[i],
			Profile:     profiles[i],
			Reasoning:   "off",
			Stream:      true,
			StartedAt:   at,
			FinishedAt:  at.Add(time.Second),
			USTCSpeedObservation: service.USTCSpeedObservation{
				TotalMS:          ustcSpeedFloat(1000),
				GenerationTPS:    metrics[i],
				OutputMS:         ustcSpeedFloat(500),
				InputTokens:      9 + i,
				InputTokensKnown: true,
				OutputTokens:     4 + i,
				TokenCountKnown:  true,
				// Keep this nullable through JSONB storage and scan.
				TTFTMS: nil,
			},
		}
		if i == 0 {
			sample.TaskID = nil
			sample.KeyScope = "physical-key-" + roundID
			sample.FinishReason = "stop"
			sample.ProviderCost = ustcSpeedFloat(0.0125)
		}
		require.NoError(t, repo.InsertSample(ctx, sample))
		require.NotZero(t, sample.ID)
		created = append(created, sample)
	}

	got, err := repo.GetSample(ctx, created[0].ID)
	require.NoError(t, err)
	require.Equal(t, created[0].ID, got.ID)
	require.Equal(t, created[0].RoundID, got.RoundID)
	require.Equal(t, created[0].AccountID, got.AccountID)
	require.Equal(t, created[0].KeyScope, got.KeyScope)
	require.Equal(t, created[0].Profile, got.Profile)
	require.Equal(t, 9, got.InputTokens)
	require.Equal(t, 4, got.OutputTokens)
	require.Equal(t, "stop", got.FinishReason)
	require.Nil(t, got.TTFTMS)
	require.NotNil(t, got.TotalMS)
	require.InDelta(t, 1000, *got.TotalMS, 0.001)
	require.NotNil(t, got.ProviderCost)
	require.InDelta(t, 0.0125, *got.ProviderCost, 1e-8)

	listed, total, err := repo.ListSamples(ctx, service.USTCSpeedQuery{
		Start:      base.Add(-time.Minute),
		End:        base.Add(5 * time.Minute),
		AccountIDs: []int64{accountID},
		Limit:      20,
	})
	require.NoError(t, err)
	require.Equal(t, int64(len(created)), total)
	require.Len(t, listed, len(created))
	require.Equal(t, created[len(created)-1].ID, listed[0].ID, "samples are listed newest first")

	missing, err := repo.GetSample(ctx, created[len(created)-1].ID+1000000)
	require.ErrorIs(t, err, service.ErrUSTCSpeedNotFound)
	require.Nil(t, missing)

	legacyID := time.Now().UnixNano()
	legacySample := &service.USTCSpeedSample{
		LegacyUsageID: &legacyID,
		RoundID:       roundID,
		AccountID:     accountID,
		AccountName:   "ustc-integration-account",
		Model:         created[0].Model,
		Source:        "real",
		Status:        "recorded",
		Profile:       "legacy:legacy_usage",
		Reasoning:     "unknown",
		StartedAt:     base.Add(time.Minute),
		FinishedAt:    base.Add(time.Minute),
		USTCSpeedObservation: service.USTCSpeedObservation{
			GenerationTPS:   ustcSpeedFloat(500),
			MeasurementMode: "legacy_usage",
		},
	}
	require.NoError(t, repo.InsertSample(ctx, legacySample))
	capturedReal := &service.USTCSpeedSample{
		RoundID: roundID, AccountID: accountID, AccountName: "ustc-integration-account",
		Model: created[0].Model, Source: "real", Status: "success", Profile: "real:chat:true",
		StartedAt: base.Add(2 * time.Minute), FinishedAt: base.Add(2 * time.Minute),
		USTCSpeedObservation: service.USTCSpeedObservation{GenerationTPS: ustcSpeedFloat(999)},
	}
	require.NoError(t, repo.InsertSample(ctx, capturedReal))
	rawReal, err := repo.Overview(ctx, service.USTCSpeedQuery{
		Start: base.Add(-time.Minute), End: base.Add(5 * time.Minute),
		AccountIDs: []int64{accountID}, Model: created[0].Model, Source: "real", Metric: "generation_tps", Interval: "raw",
	})
	require.NoError(t, err)
	require.Len(t, rawReal.Series, 1, "passively captured real samples must not be mixed into usage-record data")
	require.Equal(t, legacySample.ID, *rawReal.Series[0].SampleID)

	query := service.USTCSpeedQuery{
		Start:      base.Add(-time.Minute),
		End:        base.Add(5 * time.Minute),
		AccountIDs: []int64{accountID},
		Model:      created[0].Model,
		Source:     "probe",
		Metric:     "generation_tps",
		Interval:   "raw",
		GroupBy:    "profile",
	}
	raw, err := repo.Overview(ctx, query)
	require.NoError(t, err)
	require.Equal(t, int64(6), raw.Summary.Samples)
	require.Equal(t, int64(3), raw.Summary.Successful)
	require.Equal(t, int64(1), raw.Summary.Failed)
	require.Equal(t, int64(1), raw.Summary.Skipped)
	require.NotNil(t, raw.Summary.SuccessRate)
	require.InDelta(t, 75, *raw.Summary.SuccessRate, 0.001, "recorded and skipped rows must not affect success rate")
	require.Len(t, raw.Series, 6)
	byStatus := map[string]*float64{}
	for i, point := range raw.Series {
		byStatus[created[i].Status] = point.Value
		require.NotNil(t, point.SampleID)
		require.Equal(t, int64(1), point.Count)
		if point.Value == nil {
			require.Zero(t, point.ValidCount)
		} else {
			require.Equal(t, int64(1), point.ValidCount)
		}
	}
	require.Nil(t, byStatus["error"], "failed TPS must be NULL, not zero or the submitted value")
	require.Nil(t, byStatus["skipped"], "skipped TPS must be NULL")
	require.InDelta(t, 30, *byStatus["recorded"], 0.001, "legacy recorded metrics remain inspectable")

	query.Interval = "5m"
	query.Aggregate = "p50"
	bucketed, err := repo.Overview(ctx, query)
	require.NoError(t, err)
	require.Len(t, bucketed.Series, 2, "profiles must remain separate within one time bucket")
	a := bucketed.Series[0]
	b := bucketed.Series[1]
	require.Equal(t, "p-a", a.Profile)
	require.Equal(t, int64(5), a.Count)
	require.Equal(t, int64(3), a.ValidCount)
	require.InDelta(t, 20, *a.Mean, 0.001)
	require.InDelta(t, 12, *a.P10, 0.001)
	require.InDelta(t, 20, *a.P50, 0.001)
	require.InDelta(t, 28, *a.P90, 0.001)
	require.InDelta(t, 20, *a.Value, 0.001)
	require.Equal(t, "p-b", b.Profile)
	require.Equal(t, int64(1), b.Count)
	require.Equal(t, int64(1), b.ValidCount)
	require.InDelta(t, 100, *b.P50, 0.001)

	modelRawQuery := query
	modelRawQuery.Interval = "raw"
	modelRawQuery.GroupBy = "model"
	modelRaw, err := repo.Overview(ctx, modelRawQuery)
	require.NoError(t, err)
	require.Len(t, modelRaw.Series, len(created))
	for _, point := range modelRaw.Series {
		require.Empty(t, point.Profile, "new samples are combined in model grouping")
		require.NotNil(t, point.SampleID, "raw model grouping keeps details-link sample IDs")
	}

	modelQuery := service.USTCSpeedQuery{
		Start:      base.Add(-time.Minute),
		End:        base.Add(5 * time.Minute),
		AccountIDs: []int64{accountID},
		Model:      created[0].Model,
		Source:     "all",
		Metric:     "generation_tps",
		Interval:   "5m",
		Aggregate:  "mean",
		GroupBy:    "model",
	}
	modelOverview, err := repo.Overview(ctx, modelQuery)
	require.NoError(t, err)
	require.Equal(t, "model", modelOverview.GroupBy)
	require.Equal(t, "mean", modelOverview.Aggregate)
	require.Len(t, modelOverview.Series, 2, "probe and real sources remain independent")
	bySource := map[string]service.USTCSpeedPoint{}
	for _, point := range modelOverview.Series {
		bySource[point.Source] = point
	}
	probe := bySource["probe"]
	require.Empty(t, probe.Profile)
	require.Equal(t, int64(6), probe.Count)
	require.Equal(t, int64(4), probe.ValidCount)
	require.InDelta(t, 40, *probe.Mean, 0.001, "mean must be weighted across original valid samples from different profiles")
	require.InDelta(t, 40, *probe.Value, 0.001)
	legacy := bySource["real"]
	require.Empty(t, legacy.Profile, "model grouping merges all real-call sources")
	require.Equal(t, int64(1), legacy.Count)
	require.Equal(t, int64(1), legacy.ValidCount)
	require.InDelta(t, 500, *legacy.Value, 0.001)

	modelQuery.GroupBy = "profile"
	profileOverview, err := repo.Overview(ctx, modelQuery)
	require.NoError(t, err)
	profileSet := map[string]struct{}{}
	for _, point := range profileOverview.Series {
		profileSet[point.Profile] = struct{}{}
	}
	require.Contains(t, profileSet, "p-a")
	require.Contains(t, profileSet, "p-b")
	require.Contains(t, profileSet, "legacy:legacy_usage", "profile grouping preserves raw profile dimensions")

	modelQuery.GroupBy = "model"
	modelQuery.Profile = "legacy"
	legacyFiltered, err := repo.Overview(ctx, modelQuery)
	require.NoError(t, err)
	require.Len(t, legacyFiltered.Series, 1)
	require.Empty(t, legacyFiltered.Series[0].Profile, "the legacy profile filter still applies but model grouping does not split it out")

	autoQuery := modelQuery
	autoQuery.Profile = ""
	autoQuery.Start = base
	autoQuery.End = base.Add(24 * time.Hour)
	autoQuery.Interval = "auto"
	autoQuery.Aggregate = ""
	autoQuery.GroupBy = ""
	autoOverview, err := repo.Overview(ctx, autoQuery)
	require.NoError(t, err)
	require.Equal(t, "1m", autoOverview.Interval)
	require.Equal(t, "model", autoOverview.GroupBy)
	require.Equal(t, "mean", autoOverview.Aggregate)
}

func TestUSTCSpeedOverviewWeightsRequestsAcrossAccounts(t *testing.T) {
	ctx := context.Background()
	repo := NewUSTCSpeedRepository(integrationDB)
	roundID := "ustc-weighted-it-" + uuid.NewString()
	model := "ustc-weighted-model-" + uuid.NewString()
	base := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	accountIDs := []int64{-time.Now().UnixNano(), -time.Now().UnixNano() - 1}
	accountNames := []string{"weighted-account-a", "weighted-account-b"}
	values := []float64{10, 50, 90, 250}
	samples := make([]*service.USTCSpeedSample, 0, len(values))
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(context.Background(), `DELETE FROM ustc_speed_samples WHERE round_id=$1`, roundID)
		require.NoError(t, err)
	})
	for i, value := range values {
		accountIndex := 0
		if i == len(values)-1 {
			accountIndex = 1
		}
		at := base.Add(time.Duration(i) * 10 * time.Second)
		sample := &service.USTCSpeedSample{
			RoundID: roundID, AccountID: accountIDs[accountIndex], AccountName: accountNames[accountIndex],
			Model: model, Source: "probe", Status: "success", Profile: "profile-a",
			StartedAt: at, FinishedAt: at.Add(time.Second),
			USTCSpeedObservation: service.USTCSpeedObservation{GenerationTPS: ustcSpeedFloat(value)},
		}
		require.NoError(t, repo.InsertSample(ctx, sample))
		require.NotZero(t, sample.ID)
		samples = append(samples, sample)
	}

	query := service.USTCSpeedQuery{
		Start: base.Add(-time.Minute), End: base.Add(time.Hour), Source: "probe",
		Metric: "generation_tps", Interval: "5m",
	}
	requests, err := repo.Overview(ctx, query)
	require.NoError(t, err)
	require.Equal(t, query.Start, requests.RangeStart)
	require.Equal(t, query.End, requests.RangeEnd)
	require.Len(t, requests.Series, 1)
	requestPoint := requests.Series[0]
	require.Zero(t, requestPoint.AccountID)
	require.Empty(t, requestPoint.AccountName)
	require.Equal(t, int64(4), requestPoint.Count)
	require.Equal(t, int64(4), requestPoint.ValidCount)
	require.InDelta(t, 100, *requestPoint.Mean, 0.001, "the default mean weights all four calls equally")
	require.InDelta(t, 100, *requestPoint.Value, 0.001)
	require.InDelta(t, 22, *requestPoint.P10, 0.001, "quantiles are computed from the original call values")
	require.InDelta(t, 70, *requestPoint.P50, 0.001)
	require.InDelta(t, 202, *requestPoint.P90, 0.001)

	query.AccountIDs = []int64{accountIDs[0]}
	filteredOne, err := repo.Overview(ctx, query)
	require.NoError(t, err)
	require.Len(t, filteredOne.Series, 1)
	require.Zero(t, filteredOne.Series[0].AccountID)
	require.Empty(t, filteredOne.Series[0].AccountName)
	require.Equal(t, int64(3), filteredOne.Series[0].Count)
	require.InDelta(t, 50, *filteredOne.Series[0].Mean, 0.001, "a single-account filter still uses the common request-weighted metric")

	query.AccountIDs = accountIDs
	filteredBoth, err := repo.Overview(ctx, query)
	require.NoError(t, err)
	require.Len(t, filteredBoth.Series, 1)
	require.Equal(t, int64(4), filteredBoth.Series[0].Count)
	require.InDelta(t, 100, *filteredBoth.Series[0].Mean, 0.001, "a multi-account filter weights each matching call equally")

	query.Interval = "raw"
	raw, err := repo.Overview(ctx, query)
	require.NoError(t, err)
	require.Len(t, raw.Series, len(samples))
	seenSamples := make(map[int64]bool, len(samples))
	seenAccounts := make(map[int64]bool, len(samples))
	for _, point := range raw.Series {
		require.NotNil(t, point.SampleID)
		seenSamples[*point.SampleID] = true
		seenAccounts[point.AccountID] = true
	}
	for _, sample := range samples {
		require.True(t, seenSamples[sample.ID], "raw output must preserve each source sample")
	}
	require.True(t, seenAccounts[accountIDs[0]])
	require.True(t, seenAccounts[accountIDs[1]])

	query.Hourly = true
	query.Timezone = "UTC"
	query.Interval = "5m"
	byHour, err := repo.Overview(ctx, query)
	require.NoError(t, err)
	require.Len(t, byHour.Hourly, 1)
	require.Equal(t, 12, byHour.Hourly[0].Hour)
	require.Equal(t, int64(4), byHour.Hourly[0].Count)
	require.Equal(t, int64(4), byHour.Hourly[0].ValidCount)
	require.Equal(t, int64(1), byHour.Hourly[0].DayCount)
	require.InDelta(t, 100, *byHour.Hourly[0].Value, 0.001)

	query.Timezone = "Asia/Shanghai"
	byLocalHour, err := repo.Overview(ctx, query)
	require.NoError(t, err)
	require.Len(t, byLocalHour.Hourly, 1)
	require.Equal(t, 20, byLocalHour.Hourly[0].Hour, "hourly buckets follow the requested IANA zone")
}

func TestUSTCSpeedOverviewHourlyFoldsDaysAndUsesItsOwnMetric(t *testing.T) {
	ctx := context.Background()
	repo := NewUSTCSpeedRepository(integrationDB)
	roundID := "ustc-hourly-it-" + uuid.NewString()
	model := "ustc-hourly-model-" + uuid.NewString()
	accountID := -time.Now().UnixNano()
	start := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(context.Background(), `DELETE FROM ustc_speed_samples WHERE round_id=$1`, roundID)
		require.NoError(t, err)
	})
	seed := func(at time.Time, generation, overall float64) {
		sample := &service.USTCSpeedSample{
			RoundID: roundID, AccountID: accountID, AccountName: "hourly-account",
			Model: model, Source: "probe", Status: "success", Profile: "profile-a",
			StartedAt: at, FinishedAt: at.Add(time.Second),
			USTCSpeedObservation: service.USTCSpeedObservation{
				GenerationTPS: ustcSpeedFloat(generation),
				OverallTPS:    ustcSpeedFloat(overall),
			},
		}
		require.NoError(t, repo.InsertSample(ctx, sample))
	}
	// The out-of-range call sits in the same local hour and must not be folded in.
	seed(start.Add(-24*time.Hour+10*time.Hour+5*time.Minute), 999, 999)
	seed(start.Add(10*time.Hour+5*time.Minute), 20, 100)
	seed(start.Add(24*time.Hour+10*time.Hour+40*time.Minute), 40, 200)

	query := service.USTCSpeedQuery{
		Start: start, End: start.Add(48 * time.Hour), Source: "probe",
		Metric: "generation_tps", Interval: "1h", Timezone: "UTC",
		Hourly: true, HourlyMetric: "overall_tps",
	}
	overview, err := repo.Overview(ctx, query)
	require.NoError(t, err)
	require.Len(t, overview.Hourly, 1, "both days must fold into one hour-of-day bucket")
	hour := overview.Hourly[0]
	require.Equal(t, 10, hour.Hour)
	require.Equal(t, int64(2), hour.Count, "calls outside the applied range are excluded")
	require.Equal(t, int64(2), hour.ValidCount)
	require.Equal(t, int64(2), hour.DayCount)
	require.InDelta(t, 150, *hour.Value, 0.001, "the hourly chart uses hourly_metric, not the trend metric")
	require.InDelta(t, 150, *hour.Mean, 0.001)
	require.InDelta(t, 110, *hour.P10, 0.001)
	require.InDelta(t, 190, *hour.P90, 0.001)

	query.HourlyMetric = "generation_tps"
	byGeneration, err := repo.Overview(ctx, query)
	require.NoError(t, err)
	require.Len(t, byGeneration.Hourly, 1)
	require.InDelta(t, 30, *byGeneration.Hourly[0].Value, 0.001)
}

func TestUSTCSpeedRepositoryKeyLeaseCapacityAndOwnerRelease(t *testing.T) {
	ctx := context.Background()
	repo := NewUSTCSpeedRepository(integrationDB)
	suffix := uuid.NewString()
	scopes := []string{"ustc-key-a-" + suffix, "ustc-key-b-" + suffix}
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(context.Background(), `DELETE FROM ustc_speed_runtime WHERE key_scope=ANY($1)`, pq.Array(scopes))
		require.NoError(t, err)
	})

	start := make(chan struct{})
	type leaseResult struct {
		scope string
		owner string
		ok    bool
		err   error
	}
	results := make(chan leaseResult, len(scopes))
	for i, scope := range scopes {
		owner := fmt.Sprintf("owner-%d-%s", i, suffix)
		go func(scope, owner string) {
			<-start
			ok, err := repo.AcquireKey(ctx, scope, owner, 30*time.Second, 1)
			results <- leaseResult{scope: scope, owner: owner, ok: ok, err: err}
		}(scope, owner)
	}
	close(start)
	first, second := <-results, <-results
	require.NoError(t, first.err)
	require.NoError(t, second.err)
	require.NotEqual(t, first.ok, second.ok, "the global capacity limit must be atomic across keys")
	winner := first
	loser := second
	if !winner.ok {
		winner, loser = loser, winner
	}
	require.True(t, winner.ok)
	require.False(t, loser.ok)

	wrongOwner := winner.owner + "-other"
	require.NoError(t, repo.ReleaseKey(ctx, winner.scope, wrongOwner))
	runtime, err := repo.GetRuntime(ctx, winner.scope)
	require.NoError(t, err)
	require.NotNil(t, runtime.LeaseUntil, "a non-owner release must not clear the lease")
	require.NoError(t, repo.ReleaseKey(ctx, winner.scope, winner.owner))
	runtime, err = repo.GetRuntime(ctx, winner.scope)
	require.NoError(t, err)
	require.Nil(t, runtime.LeaseUntil)

	// After the owner releases its lease, the waiting physical key can acquire
	// the freed global slot.
	got, err := repo.AcquireKey(ctx, loser.scope, "retry-"+suffix, 30*time.Second, 1)
	require.NoError(t, err)
	require.True(t, got)
	require.NoError(t, repo.ReleaseKey(ctx, loser.scope, "retry-"+suffix))
}

func TestUSTCSpeedRepositoryLegacyImportBoundariesAndBatches(t *testing.T) {
	ctx := context.Background()
	repo := NewUSTCSpeedRepository(integrationDB)
	suffix := uuid.NewString()
	user := mustCreateUser(t, integrationEntClient, &service.User{Email: "ustc-legacy-" + suffix + "@example.com"})
	apiKey := mustCreateApiKey(t, integrationEntClient, &service.APIKey{UserID: user.ID, Key: "sk-ustc-legacy-" + suffix, Name: "ustc-legacy"})
	accounts := []*service.Account{
		mustCreateAccount(t, integrationEntClient, &service.Account{Name: "ustc-legacy-a-" + suffix}),
		mustCreateAccount(t, integrationEntClient, &service.Account{Name: "ustc-legacy-b-" + suffix}),
	}
	accountIDs := []int64{accounts[0].ID, accounts[1].ID}
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(context.Background(), `DELETE FROM ustc_speed_samples WHERE account_id=ANY($1)`, pq.Array(accountIDs))
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(context.Background(), `DELETE FROM usage_logs WHERE account_id=ANY($1)`, pq.Array(accountIDs))
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(context.Background(), `DELETE FROM api_keys WHERE id=$1`, apiKey.ID)
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(context.Background(), `DELETE FROM accounts WHERE id=ANY($1)`, pq.Array(accountIDs))
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(context.Background(), `DELETE FROM users WHERE id=$1`, user.ID)
		require.NoError(t, err)
	})

	after := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	before := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	createdAt := []time.Time{
		after.Add(-time.Microsecond),  // excluded: lower bound is inclusive
		after,                         // included at lower bound
		after.Add(time.Hour),          // included
		before.Add(-time.Microsecond), // included just below upper bound
		before,                        // excluded: upper bound is exclusive
	}
	expectedLegacyIDs := map[int64]int64{}
	for accountIndex, account := range accounts {
		for rowIndex, at := range createdAt {
			upstreamModel := ""
			if rowIndex == 2 {
				upstreamModel = "actual-upstream-model"
			}
			var id int64
			err := integrationDB.QueryRowContext(ctx, `INSERT INTO usage_logs(
				user_id,api_key_id,account_id,request_id,model,upstream_model,input_tokens,output_tokens,
				cache_read_tokens,stream,duration_ms,first_token_ms,created_at)
				VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) RETURNING id`,
				user.ID, apiKey.ID, account.ID, fmt.Sprintf("ustc-%s-%d-%d", suffix, accountIndex, rowIndex),
				"requested-model", upstreamModel, 10+rowIndex, 3+rowIndex, 2, rowIndex%2 == 0,
				1000+rowIndex*100, 100+rowIndex*10, at,
			).Scan(&id)
			require.NoError(t, err)
			if rowIndex >= 1 && rowIndex <= 3 {
				expectedLegacyIDs[id] = account.ID
			}
		}
	}

	for _, want := range []int64{2, 2, 2, 0} {
		imported, err := repo.ImportUsage(ctx, before, after, accountIDs, 2)
		require.NoError(t, err)
		require.Equal(t, want, imported, "each bounded import call should advance without duplicating prior batches")
	}

	rows, err := integrationDB.QueryContext(ctx, `SELECT legacy_usage_id,account_id,source,status,profile,model,started_at FROM ustc_speed_samples WHERE account_id=ANY($1) AND legacy_usage_id IS NOT NULL ORDER BY legacy_usage_id`, pq.Array(accountIDs))
	require.NoError(t, err)
	defer rows.Close()
	seen := make(map[int64]int64)
	modelByLegacyID := make(map[int64]string)
	legacyProfiles := make(map[string]struct{})
	for rows.Next() {
		var legacyID, accountID int64
		var source, status, profile, model string
		var startedAt time.Time
		require.NoError(t, rows.Scan(&legacyID, &accountID, &source, &status, &profile, &model, &startedAt))
		seen[legacyID] = accountID
		modelByLegacyID[legacyID] = model
		legacyProfiles[profile] = struct{}{}
		require.Equal(t, "real", source)
		require.Equal(t, "recorded", status)
		require.True(t, strings.HasPrefix(profile, "legacy:"), "legacy samples must expose the input/output profile dimensions")
		require.GreaterOrEqual(t, startedAt, after)
		require.Less(t, startedAt, before)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, expectedLegacyIDs, seen, "inclusive/exclusive bounds and every per-account batch must be preserved")
	require.GreaterOrEqual(t, len(legacyProfiles), 2, "different stream/input/output combinations must not share one legacy profile")
	for legacyID := range expectedLegacyIDs {
		if modelByLegacyID[legacyID] == "actual-upstream-model" {
			continue
		}
		// The other records have NULL/empty upstream_model and should fall back
		// to the requested model.
		require.Equal(t, "requested-model", modelByLegacyID[legacyID])
	}
	var upstreamCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM ustc_speed_samples WHERE account_id=ANY($1) AND legacy_usage_id IS NOT NULL AND model='actual-upstream-model'`, pq.Array(accountIDs)).Scan(&upstreamCount))
	require.Equal(t, 2, upstreamCount, "one upstream-model usage row per account should retain the mapped model")

	overview, err := repo.Overview(ctx, service.USTCSpeedQuery{
		Start:      after,
		End:        before,
		AccountIDs: accountIDs,
		Source:     "real",
		Metric:     "total_ms",
		Interval:   "raw",
	})
	require.NoError(t, err)
	require.Equal(t, int64(6), overview.Summary.Samples)
	require.Equal(t, int64(0), overview.Summary.Successful)
	require.Nil(t, overview.Summary.SuccessRate, "recorded legacy imports must not count in success-rate denominator")
	for _, point := range overview.Series {
		require.Empty(t, point.Profile, "model grouping merges all real-call sources")
		require.NotNil(t, point.SampleID)
	}

	legacyProfileOverview, err := repo.Overview(ctx, service.USTCSpeedQuery{
		Start:      after,
		End:        before,
		AccountIDs: accountIDs,
		Source:     "real",
		Metric:     "total_ms",
		Interval:   "raw",
		GroupBy:    "profile",
	})
	require.NoError(t, err)
	for _, point := range legacyProfileOverview.Series {
		require.True(t, strings.HasPrefix(point.Profile, "legacy:"), "profile grouping retains detailed usage-record dimensions")
	}

	recentAt := before.Add(time.Hour)
	var recentID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO usage_logs(
		user_id,api_key_id,account_id,request_id,model,input_tokens,output_tokens,stream,duration_ms,first_token_ms,created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id`,
		user.ID, apiKey.ID, accounts[0].ID, "ustc-recent-"+suffix,
		"requested-model", 10, 20, true, 1500, 200, recentAt,
	).Scan(&recentID))
	imported, err := repo.ImportRecentUsage(ctx, recentAt.Add(time.Hour), recentAt, accountIDs, 10)
	require.NoError(t, err)
	require.Equal(t, int64(1), imported, "new usage records must be picked up by the continuous importer")
	imported, err = repo.ImportRecentUsage(ctx, recentAt.Add(time.Hour), recentAt, accountIDs, 10)
	require.NoError(t, err)
	require.Zero(t, imported, "re-importing the same usage record must not duplicate it")
	var recentSamples int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM ustc_speed_samples WHERE legacy_usage_id=$1`, recentID).Scan(&recentSamples))
	require.Equal(t, 1, recentSamples)
}

func TestUSTCSpeedRepositoryEditedTaskKeepsNewSchedule(t *testing.T) {
	ctx := context.Background()
	repo := NewUSTCSpeedRepository(integrationDB)
	task := service.DefaultUSTCSpeedTask()
	task.Name = "revision-" + uuid.NewString()
	require.NoError(t, repo.SaveTask(ctx, &task))
	t.Cleanup(func() { require.NoError(t, repo.DeleteTask(context.Background(), task.ID)) })
	require.Equal(t, int64(1), task.Revision)
	originalRevision := task.Revision
	claimed, err := repo.ClaimTask(ctx, task.ID, "old-run", time.Minute, true, originalRevision)
	require.NoError(t, err)
	require.True(t, claimed)

	newSchedule := time.Now().Add(3 * time.Hour).Truncate(time.Microsecond)
	task.NextRunAt = &newSchedule
	task.CronExpression = "0 */3 * * *"
	require.NoError(t, repo.SaveTask(ctx, &task))
	require.Equal(t, originalRevision+1, task.Revision)
	claimed, err = repo.ClaimTask(ctx, task.ID, "new-run", time.Minute, true, task.Revision)
	require.NoError(t, err)
	require.False(t, claimed, "an edit must not allow overlapping old and new runs")
	require.NoError(t, repo.FinishTask(ctx, task.ID, "old-run", time.Now().Add(time.Minute), "completed", "stale_detail"))
	got, err := repo.GetTask(ctx, task.ID)
	require.NoError(t, err)
	require.True(t, newSchedule.Equal(*got.NextRunAt))
	require.Equal(t, "cancelled", got.LastStatus)
	require.Empty(t, got.LastDetail, "a stale run must not overwrite the newer revision detail")
	require.Equal(t, task.CronExpression, got.CronExpression)

	claimed, err = repo.ClaimTask(ctx, task.ID, "stale-reader", time.Minute, true, originalRevision)
	require.NoError(t, err)
	require.False(t, claimed, "a read-before-edit task cannot claim the new revision")
	claimed, err = repo.ClaimTask(ctx, task.ID, "new-run", time.Minute, true, got.Revision)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, repo.FinishTask(ctx, task.ID, "old-run", time.Now(), "failed", "stale_detail"))
	claimed, err = repo.ClaimTask(ctx, task.ID, "other-run", time.Minute, true, got.Revision)
	require.NoError(t, err)
	require.False(t, claimed, "an old owner cannot release the new owner's lease")
	next := newSchedule.Add(time.Hour)
	require.NoError(t, repo.FinishTask(ctx, task.ID, "new-run", next, "completed", "user_load_or_capacity×2"))
	got, err = repo.GetTask(ctx, task.ID)
	require.NoError(t, err)
	require.True(t, next.Equal(*got.NextRunAt))
	require.Equal(t, "completed", got.LastStatus)
	require.Equal(t, "user_load_or_capacity×2", got.LastDetail)
}

func ustcSpeedFloat(value float64) *float64 {
	return &value
}
