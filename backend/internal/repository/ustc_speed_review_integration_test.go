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

func TestUSTCSpeedBucketsFollowLocalTimeAndCalendarDayEnd(t *testing.T) {
	for _, tc := range []struct{ zone, interval, at, start, end string }{
		{"Asia/Shanghai", "1d", "2026-10-26T12:40:00+08:00", "2026-10-26T00:00:00+08:00", "2026-10-27T00:00:00+08:00"},
		{"Asia/Shanghai", "6h", "2026-10-26T13:40:00+08:00", "2026-10-26T12:00:00+08:00", "2026-10-26T18:00:00+08:00"},
		{"Asia/Kolkata", "1h", "2026-10-26T13:40:00+05:30", "2026-10-26T13:00:00+05:30", "2026-10-26T14:00:00+05:30"},
		{"America/New_York", "1d", "2026-03-08T13:40:00-04:00", "2026-03-08T00:00:00-05:00", "2026-03-09T00:00:00-04:00"},
		{"America/New_York", "1d", "2026-11-01T13:40:00-05:00", "2026-11-01T00:00:00-04:00", "2026-11-02T00:00:00-05:00"},
		{"America/New_York", "1h", "2026-11-01T01:30:00-04:00", "2026-11-01T01:00:00-04:00", "2026-11-01T01:00:00-05:00"},
		{"America/New_York", "1h", "2026-11-01T01:30:00-05:00", "2026-11-01T01:00:00-05:00", "2026-11-01T02:00:00-05:00"},
	} {
		t.Run(tc.zone+"/"+tc.interval+"/"+tc.at, func(t *testing.T) {
			ctx := context.Background()
			repo := NewUSTCSpeedRepository(integrationDB)
			model := "zone-" + uuid.NewString()
			parse := func(s string) time.Time { v, err := time.Parse(time.RFC3339, s); require.NoError(t, err); return v }
			at, start, end := parse(tc.at), parse(tc.start), parse(tc.end)
			t.Cleanup(func() {
				_, err := integrationDB.ExecContext(ctx, `DELETE FROM ustc_speed_samples WHERE model=$1`, model)
				require.NoError(t, err)
			})
			require.NoError(t, repo.InsertSample(ctx, &service.USTCSpeedSample{AccountID: 1, Model: model, Source: "probe", Status: "success", Stream: true, StartedAt: at, FinishedAt: at.Add(time.Second), USTCSpeedObservation: service.USTCSpeedObservation{OutputTokens: 10, TokenCountKnown: true, TotalMS: ustcSpeedFloat(1000)}}))
			out, err := repo.Overview(ctx, service.USTCSpeedQuery{Start: start, End: end, Source: "probe", Model: model, Metric: "overall_tps", Interval: tc.interval, Timezone: tc.zone})
			require.NoError(t, err)
			require.Len(t, out.Series, 1)
			require.True(t, start.Equal(out.Series[0].Time), "got %s, want %s", out.Series[0].Time, start)
			require.NotNil(t, out.Series[0].BucketEnd)
			require.True(t, end.Equal(*out.Series[0].BucketEnd), "got %s, want %s", out.Series[0].BucketEnd, end)
		})
	}
}

func TestUSTCSpeedTruncationKeepsNewestBucketsAndFlagsHourly(t *testing.T) {
	ctx := context.Background()
	repo := NewUSTCSpeedRepository(integrationDB)
	model := "latest-" + uuid.NewString()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(ctx, `DELETE FROM ustc_speed_samples WHERE model=$1`, model)
		require.NoError(t, err)
	})
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO ustc_speed_samples(account_id,model,source,status,profile,started_at,finished_at,payload)
 SELECT 1,$1,'probe','success','p-'||i,$2::timestamptz+i*INTERVAL '1 minute',$2::timestamptz+i*INTERVAL '1 minute','{"generation_tps":30}'::jsonb FROM generate_series(0,5001) i`, model, base)
	require.NoError(t, err)
	q := service.USTCSpeedQuery{Start: base, End: base.Add(5003 * time.Minute), Source: "probe", Model: model, Metric: "generation_tps", Interval: "1m", Timezone: "UTC"}
	out, err := repo.Overview(ctx, q)
	require.NoError(t, err)
	require.True(t, out.Truncated)
	require.Len(t, out.Series, 5000)
	require.Equal(t, base.Add(2*time.Minute), out.Series[0].Time.UTC())
	require.Equal(t, base.Add(5001*time.Minute), out.Series[4999].Time.UTC())
	q.GroupBy, q.Hourly = "profile", true
	out, err = repo.Overview(ctx, q)
	require.NoError(t, err)
	require.True(t, out.HourlyTruncated)
	require.Len(t, out.Hourly, 5000)
}

func TestUSTCSpeedRatesNormalizeHistoricalFormulasAndRejectOneToken(t *testing.T) {
	ctx := context.Background()
	repo := NewUSTCSpeedRepository(integrationDB)
	model := "rate-" + uuid.NewString()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(ctx, `DELETE FROM ustc_speed_samples WHERE model=$1`, model)
		require.NoError(t, err)
	})
	ids := []int64{}
	for _, tokens := range []int{2, 1} {
		sample := &service.USTCSpeedSample{AccountID: 1, Model: model, Source: "probe", Status: "success", Stream: true, StartedAt: base, FinishedAt: base.Add(time.Second), USTCSpeedObservation: service.USTCSpeedObservation{OutputTokens: tokens, TokenCountKnown: true, TotalMS: ustcSpeedFloat(1000), FirstOutputMS: ustcSpeedFloat(200), GenerationTPS: ustcSpeedFloat(999), OverallTPS: ustcSpeedFloat(999)}}
		require.NoError(t, repo.InsertSample(ctx, sample))
		ids = append(ids, sample.ID)
	}
	q := service.USTCSpeedQuery{Start: base, End: base.Add(time.Hour), Source: "probe", Model: model, Metric: "generation_tps", Interval: "raw", Analysis: true, Hourly: true, Timezone: "UTC"}
	out, err := repo.Overview(ctx, q)
	require.NoError(t, err)
	require.InDelta(t, 2.5, *out.Series[0].Value, 1e-9)
	require.Nil(t, out.Series[1].Value)
	require.EqualValues(t, 1, out.Analysis.Groups[0].ValidCount)
	raw, err := repo.GetSample(ctx, ids[0])
	require.NoError(t, err)
	require.InDelta(t, 2.5, *raw.GenerationTPS, 1e-9)
	var stored float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT (payload->>'generation_tps')::float8 FROM ustc_speed_samples WHERE id=$1`, ids[0]).Scan(&stored))
	require.Equal(t, float64(999), stored, "normalizing a derived rate must not rewrite raw history")
	q.Metric = "overall_tps"
	out, err = repo.Overview(ctx, q)
	require.NoError(t, err)
	require.Nil(t, out.Series[1].Value)
}

func TestUSTCSpeedOrdinaryEditPreservesRunningSnapshotAndConcurrentFinish(t *testing.T) {
	ctx := context.Background()
	repo := NewUSTCSpeedRepository(integrationDB)
	task := service.DefaultUSTCSpeedTask()
	task.Name = "edit-snapshot-" + uuid.NewString()
	require.NoError(t, repo.SaveTask(ctx, &task))
	t.Cleanup(func() { require.NoError(t, repo.DeleteTask(ctx, task.ID)) })
	originalNext := *task.NextRunAt
	claimed, err := repo.ClaimTask(ctx, task.ID, "snapshot-owner", time.Minute, true, task.Revision)
	require.NoError(t, err)
	require.True(t, claimed)
	task.Prompt = "updated prompt"
	require.NoError(t, repo.SaveTask(ctx, &task))
	require.True(t, originalNext.Equal(*task.NextRunAt))
	require.Equal(t, "preparing", task.Progress.State)
	// The editor read this value before the running snapshot finished.
	staleEditor := task
	next := time.Now().Add(20 * time.Minute).Truncate(time.Microsecond)
	require.NoError(t, repo.FinishTask(ctx, task.ID, "snapshot-owner", next, "completed", "actual run result"))
	staleEditor.Name = "renamed after finish"
	require.NoError(t, repo.SaveTask(ctx, &staleEditor))
	require.True(t, next.Equal(*staleEditor.NextRunAt), "ordinary saves preserve a concurrently advanced plan")
	require.Equal(t, "completed", staleEditor.LastStatus)
	require.Equal(t, "completed", staleEditor.Progress.State)
	require.Equal(t, "actual run result", staleEditor.LastDetail)
}
