//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestUsageSpeedReadsOriginalRecordsWithSharedFilters(t *testing.T) {
	ctx := context.Background()
	suffix := uuid.NewString()
	user := mustCreateUser(t, integrationEntClient, &service.User{Email: "speed-" + suffix + "@example.com"})
	key := mustCreateApiKey(t, integrationEntClient, &service.APIKey{UserID: user.ID, Key: "sk-speed-" + suffix, Name: "speed"})
	account := mustCreateAccount(t, integrationEntClient, &service.Account{Name: "speed-" + suffix})
	group := mustCreateGroup(t, integrationEntClient, &service.Group{Name: "speed-" + suffix})
	t.Cleanup(func() {
		for _, cleanup := range []struct {
			query string
			id    int64
		}{
			{"DELETE FROM usage_logs WHERE user_id=$1", user.ID},
			{"DELETE FROM api_keys WHERE id=$1", key.ID},
			{"DELETE FROM accounts WHERE id=$1", account.ID},
			{"DELETE FROM users WHERE id=$1", user.ID},
			{"DELETE FROM groups WHERE id=$1", group.ID},
		} {
			_, err := integrationDB.ExecContext(ctx, cleanup.query, cleanup.id)
			require.NoError(t, err)
		}
	})
	base := time.Date(2025, 10, 1, 0, 0, 0, 0, time.UTC)
	model := "client-" + suffix
	ids := []int64{}
	for i := 0; i < 5; i++ {
		at := base.Add(time.Duration(i+1) * time.Hour)
		stream, output, duration, kind, mode := true, 100, 1100, int16(service.RequestTypeUnknown), "token"
		if i == 1 {
			output, duration, kind = 200, 2100, int16(service.RequestTypeStream)
		}
		if i == 2 {
			stream, output, duration, kind, mode = false, 50, 1000, int16(service.RequestTypeSync), "per_request"
		}
		if i == 3 {
			at, output = base.Add(-time.Hour), 50
		}
		image := 0
		if i == 4 {
			image = 1
		}
		var id int64
		err := integrationDB.QueryRowContext(ctx, `INSERT INTO usage_logs(user_id,api_key_id,account_id,group_id,request_id,model,requested_model,upstream_model,input_tokens,output_tokens,stream,duration_ms,first_token_ms,request_type,native_compaction_v2,billing_type,billing_mode,upstream_model_mismatch,image_count,image_size,created_at)
 VALUES($1,$2,$3,$4,$5,'raw-model',$6,'upstream-model',20,$7,$8,$9,100,$10,$11,$12,$13,$14,$15,'1K',$16) RETURNING id`,
			user.ID, key.ID, account.ID, group.ID, fmt.Sprintf("speed-%s-%d", suffix, i), model, output, stream, duration, kind, i == 1, i%2, mode, i == 1, image, at).Scan(&id)
		require.NoError(t, err)
		ids = append(ids, id)
	}
	repo := NewUSTCSpeedRepository(integrationDB)
	q := service.USTCSpeedQuery{Start: base, End: base.Add(24 * time.Hour), Source: "real", Model: model, Metric: "generation_tps", Interval: "raw", Hourly: true, Timezone: "UTC", Analysis: true, UsageFilters: &usagestats.UsageLogFilters{UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, GroupID: group.ID}}
	out, err := repo.Overview(ctx, q)
	require.NoError(t, err)
	require.EqualValues(t, 3, out.Summary.Samples, "images and out-of-range calls are excluded")
	require.Len(t, out.Series, 3)
	require.Equal(t, ids[0], *out.Series[0].SampleID, "raw points retain original usage IDs")
	require.Equal(t, model, out.Series[0].Model, "the requested model agrees with usage-detail filters")
	require.InDelta(t, 100, *out.Series[0].Value, 1e-8)
	require.InDelta(t, 100, *out.Series[1].Value, 1e-8)
	require.Nil(t, out.Series[2].Value, "nonstreaming calls have no generation TPS")
	for _, row := range out.Analysis.Groups {
		if row.Period == "current" {
			require.EqualValues(t, 2, row.ValidCount)
			require.InDelta(t, 100, *row.Mean, 1e-8)
		}
		if row.Period == "previous" {
			require.EqualValues(t, 1, row.ValidCount)
			require.InDelta(t, 50, *row.Mean, 1e-8)
		}
	}
	streamType := int16(service.RequestTypeStream)
	trueValue := true
	paid := int8(1)
	for _, tc := range []struct {
		name   string
		filter usagestats.UsageLogFilters
		want   int64
	}{
		{"legacy and typed stream", usagestats.UsageLogFilters{UserID: user.ID, RequestType: &streamType}, 2},
		{"native compaction", usagestats.UsageLogFilters{UserID: user.ID, NativeCompactionV2: &trueValue}, 1},
		{"billing type", usagestats.UsageLogFilters{UserID: user.ID, BillingType: &paid}, 1},
		{"billing mode", usagestats.UsageLogFilters{UserID: user.ID, BillingMode: "per_request"}, 1},
		{"model mismatch", usagestats.UsageLogFilters{UserID: user.ID, UpstreamModelMismatch: &trueValue}, 1},
		{"request id", usagestats.UsageLogFilters{RequestID: fmt.Sprintf("speed-%s-0", suffix)}, 1},
		{"wrong account", usagestats.UsageLogFilters{UserID: user.ID, AccountID: account.ID + 1000000}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q.UsageFilters, q.Analysis = &tc.filter, false
			data, err := repo.Overview(ctx, q)
			require.NoError(t, err)
			require.Equal(t, tc.want, data.Summary.Samples)
		})
	}
	q.UsageFilters = &usagestats.UsageLogFilters{UserID: user.ID}
	q.Metric = "overall_tps"
	out, err = repo.Overview(ctx, q)
	require.NoError(t, err)
	require.NotNil(t, out.Series[2].Value, "nonstreaming calls still have overall TPS")
	var copies int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM ustc_speed_samples WHERE legacy_usage_id=ANY($1)`, pq.Array(ids)).Scan(&copies))
	require.Zero(t, copies, "analysis is read-only and never imports usage records")
}
