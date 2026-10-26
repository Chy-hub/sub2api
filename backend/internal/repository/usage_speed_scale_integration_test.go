//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// This fixture exercises the real SQL projection and the complete page query,
// including hourly folding and analysis; it never calls an upstream model.
func TestUsageSpeedLargeHistoryQuery(t *testing.T) {
	ctx := context.Background()
	suffix := uuid.NewString()
	user := mustCreateUser(t, integrationEntClient, &service.User{Email: "speed-scale-" + suffix + "@example.com"})
	key := mustCreateApiKey(t, integrationEntClient, &service.APIKey{UserID: user.ID, Key: "sk-scale-" + suffix, Name: "scale"})
	account := mustCreateAccount(t, integrationEntClient, &service.Account{Name: "scale-" + suffix})
	t.Cleanup(func() {
		for _, item := range []struct {
			sql string
			id  int64
		}{
			{"DELETE FROM usage_logs WHERE user_id=$1", user.ID}, {"DELETE FROM api_keys WHERE id=$1", key.ID},
			{"DELETE FROM accounts WHERE id=$1", account.ID}, {"DELETE FROM users WHERE id=$1", user.ID},
		} {
			_, err := integrationDB.ExecContext(ctx, item.sql, item.id)
			require.NoError(t, err)
		}
	})
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO usage_logs(user_id,api_key_id,account_id,request_id,model,requested_model,input_tokens,output_tokens,stream,duration_ms,first_token_ms,created_at)
 SELECT $1,$2,$3,'scale-'||$4::text||'-'||i,'scale-model-'||(i%10),'scale-model-'||(i%10),1024+(i%4096),400+(i%600),true,1000+(i%4000),80,$5::timestamptz+i*INTERVAL '5 minutes' FROM generate_series(0,49999) i`, user.ID, key.ID, account.ID, suffix, base)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `ANALYZE usage_logs`)
	require.NoError(t, err)
	repo := NewUSTCSpeedRepository(integrationDB)
	end := base.Add(50000 * 5 * time.Minute)
	q := service.USTCSpeedQuery{End: end, Source: "real", Metric: "overall_tps", Interval: "auto", Hourly: true, Timezone: "Asia/Shanghai", UsageFilters: &usagestats.UsageLogFilters{UserID: user.ID}}
	for _, tc := range []struct {
		name     string
		start    time.Time
		analysis bool
		count    int64
	}{
		{"24h trend and hourly", end.Add(-24 * time.Hour), false, 288},
		{"180d complete analysis", end.Add(-180 * 24 * time.Hour), true, 50000},
		{"365d complete analysis", end.Add(-365 * 24 * time.Hour), true, 50000},
	} {
		q.Start, q.Analysis = tc.start, tc.analysis
		started := time.Now()
		out, err := repo.Overview(ctx, q)
		require.NoError(t, err)
		require.Equal(t, tc.count, out.Summary.Samples)
		require.NotEmpty(t, out.Series)
		require.NotEmpty(t, out.Hourly)
		require.LessOrEqual(t, len(out.Hourly), 240)
		var hourlyCount int64
		for _, point := range out.Hourly {
			hourlyCount += point.ValidCount
		}
		require.Equal(t, tc.count, hourlyCount, "hourly folding preserves every valid call")
		if tc.analysis {
			require.Len(t, out.Analysis.Groups, 10)
		}
		t.Logf("%s: %s, rows=%d, trend=%d, hourly=%d", tc.name, time.Since(started).Round(time.Millisecond), out.Summary.Samples, len(out.Series), len(out.Hourly))
	}
	where, args := ustcSpeedWhere(q)
	rows, err := integrationDB.QueryContext(ctx, `EXPLAIN (ANALYZE,BUFFERS) SELECT AVG(`+speedMetricValue(q, q.Metric)+`) FROM `+speedObservationTable(q)+` WHERE `+where, args...)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var line string
		require.NoError(t, rows.Scan(&line))
		t.Log(line)
	}
	require.NoError(t, rows.Err())
}
