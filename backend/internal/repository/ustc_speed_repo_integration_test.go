//go:build integration

package repository

import (
	"context"
	"fmt"
	"strings"
	"sync"
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

	query := service.USTCSpeedQuery{
		Start:      base.Add(-time.Minute),
		End:        base.Add(5 * time.Minute),
		AccountIDs: []int64{accountID},
		Model:      created[0].Model,
		Source:     "probe",
		Metric:     "generation_tps",
		Interval:   "raw",
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

func TestUSTCSpeedRepositoryReserveBudgetIsAtomic(t *testing.T) {
	ctx := context.Background()
	repo := NewUSTCSpeedRepository(integrationDB)
	taskID := time.Now().UnixNano()
	taskBudgetKey := fmt.Sprintf("task:%d", taskID)
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(context.Background(), `DELETE FROM ustc_speed_daily_budget WHERE day=(NOW() AT TIME ZONE 'UTC')::date AND budget_key IN ('global',$1)`, taskBudgetKey)
		require.NoError(t, err)
	})

	ok, err := repo.ReserveBudget(ctx, taskID, 1, 80, 2, 100, 5, 150)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = repo.ReserveBudget(ctx, taskID, 1, 30, 2, 100, 5, 150)
	require.NoError(t, err)
	require.False(t, ok, "a task token overrun must reject the whole reservation")
	assertUSTCBudget(t, "global", 1, 80)
	assertUSTCBudget(t, taskBudgetKey, 1, 80)
	ok, err = repo.ReserveBudget(ctx, taskID, 1, 20, 2, 100, 5, 150)
	require.NoError(t, err)
	require.True(t, ok)

	const concurrentCalls = 12
	results := make(chan error, concurrentCalls)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range concurrentCalls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			reserved, reserveErr := repo.ReserveBudget(ctx, taskID, 1, 10, 10, 500, 5, 150)
			if reserveErr != nil {
				results <- reserveErr
				return
			}
			if reserved {
				results <- nil
				return
			}
			results <- errUSTCBudgetRejected
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	reservedCount := 0
	for reserveErr := range results {
		if reserveErr == nil {
			reservedCount++
		} else {
			require.ErrorIs(t, reserveErr, errUSTCBudgetRejected)
		}
	}
	require.Equal(t, 3, reservedCount, "only the three remaining global request slots may be reserved")
	assertUSTCBudget(t, "global", 5, 130)
	assertUSTCBudget(t, taskBudgetKey, 5, 130)
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
	require.NoError(t, repo.FinishTask(ctx, task.ID, "old-run", time.Now().Add(time.Minute), "completed"))
	got, err := repo.GetTask(ctx, task.ID)
	require.NoError(t, err)
	require.True(t, newSchedule.Equal(*got.NextRunAt))
	require.Equal(t, "cancelled", got.LastStatus)
	require.Equal(t, task.CronExpression, got.CronExpression)

	claimed, err = repo.ClaimTask(ctx, task.ID, "stale-reader", time.Minute, true, originalRevision)
	require.NoError(t, err)
	require.False(t, claimed, "a read-before-edit task cannot claim the new revision")
	claimed, err = repo.ClaimTask(ctx, task.ID, "new-run", time.Minute, true, got.Revision)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, repo.FinishTask(ctx, task.ID, "old-run", time.Now(), "failed"))
	claimed, err = repo.ClaimTask(ctx, task.ID, "other-run", time.Minute, true, got.Revision)
	require.NoError(t, err)
	require.False(t, claimed, "an old owner cannot release the new owner's lease")
	next := newSchedule.Add(time.Hour)
	require.NoError(t, repo.FinishTask(ctx, task.ID, "new-run", next, "completed"))
	got, err = repo.GetTask(ctx, task.ID)
	require.NoError(t, err)
	require.True(t, next.Equal(*got.NextRunAt))
	require.Equal(t, "completed", got.LastStatus)
}

func TestUSTCSpeedRepositoryCleanupDrainsMultipleBatches(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	repo := NewUSTCSpeedRepository(integrationDB)
	accountID := -time.Now().UnixNano()
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(context.Background(), `DELETE FROM ustc_speed_samples WHERE account_id=$1`, accountID)
		require.NoError(t, err)
	})
	before := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO ustc_speed_samples(account_id,source,status,started_at,finished_at) SELECT $1,'real','recorded',$2::timestamptz-INTERVAL '1 day',$2::timestamptz-INTERVAL '1 day' FROM generate_series(1,10003)`, accountID, before)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO ustc_speed_samples(account_id,source,status,started_at,finished_at) VALUES($1,'real','recorded',$2,$2)`, accountID, before)
	require.NoError(t, err)
	require.NoError(t, repo.Cleanup(ctx, before))
	var oldCount, remaining int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FILTER(WHERE started_at<$2),COUNT(*) FROM ustc_speed_samples WHERE account_id=$1`, accountID, before).Scan(&oldCount, &remaining))
	require.Zero(t, oldCount, "retention must drain all expired batches within the deadline")
	require.Equal(t, 1, remaining, "the retention boundary is exclusive")
}

func assertUSTCBudget(t *testing.T, key string, requests, tokens int64) {
	t.Helper()
	var gotRequests, gotTokens int64
	err := integrationDB.QueryRowContext(context.Background(), `SELECT requests,output_tokens FROM ustc_speed_daily_budget WHERE day=(NOW() AT TIME ZONE 'UTC')::date AND budget_key=$1`, key).Scan(&gotRequests, &gotTokens)
	require.NoError(t, err)
	require.Equal(t, requests, gotRequests)
	require.Equal(t, tokens, gotTokens)
}

func ustcSpeedFloat(value float64) *float64 {
	return &value
}

var errUSTCBudgetRejected = fmt.Errorf("budget reservation rejected")
