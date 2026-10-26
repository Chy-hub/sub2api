package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

type ustcSpeedRepository struct{ db *sql.DB }

func NewUSTCSpeedRepository(db *sql.DB) service.USTCSpeedRepository {
	return &ustcSpeedRepository{db: db}
}

func (r *ustcSpeedRepository) GetSettings(ctx context.Context) (*service.USTCSpeedSettings, time.Time, error) {
	var raw []byte
	var since time.Time
	err := r.db.QueryRowContext(ctx, `SELECT settings,capture_since FROM ustc_speed_config WHERE id=1`).Scan(&raw, &since)
	if err != nil {
		return nil, since, err
	}
	cfg := service.DefaultUSTCSpeedSettings()
	if err = json.Unmarshal(raw, &cfg); err != nil {
		return nil, since, err
	}
	return &cfg, since, nil
}
func (r *ustcSpeedRepository) SaveSettings(ctx context.Context, cfg service.USTCSpeedSettings) error {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `UPDATE ustc_speed_config SET settings=$1,updated_at=NOW() WHERE id=1`, raw)
	return err
}
func (r *ustcSpeedRepository) InsertSample(ctx context.Context, s *service.USTCSpeedSample) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, `INSERT INTO ustc_speed_samples(legacy_usage_id,task_id,round_id,account_id,account_name,key_scope,model,source,status,profile,reasoning,stream,started_at,finished_at,payload)
 VALUES($1,(SELECT id FROM ustc_speed_tasks WHERE id=$2),$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
 ON CONFLICT(legacy_usage_id) DO NOTHING RETURNING id`, s.LegacyUsageID, s.TaskID, s.RoundID, s.AccountID, s.AccountName, s.KeyScope, s.Model, s.Source, s.Status, s.Profile, s.Reasoning, s.Stream, s.StartedAt, s.FinishedAt, raw).Scan(&s.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if s.KeyScope != "" && s.LegacyUsageID == nil && (s.Status == "success" || s.Status == "rate_limited" || s.Status == "error" || s.Status == "cancelled") {
		_, err = tx.ExecContext(ctx, `INSERT INTO ustc_speed_runtime(key_scope,recent_429,consecutive_429,success_streak,last_429_at,last_status)
   VALUES($1,CASE WHEN $2='rate_limited' THEN ARRAY[$3::bigint] ELSE '{}'::bigint[] END,CASE WHEN $2='rate_limited' THEN 1 ELSE 0 END,CASE WHEN $2='success' THEN 1 ELSE 0 END,CASE WHEN $2='rate_limited' THEN $4::timestamptz ELSE NULL END,$2)
   ON CONFLICT(key_scope) DO UPDATE SET
   recent_429=CASE WHEN $2='success' THEN '{}'::bigint[] WHEN $2='rate_limited' THEN COALESCE(ustc_speed_runtime.recent_429[GREATEST(1,cardinality(ustc_speed_runtime.recent_429)-48):cardinality(ustc_speed_runtime.recent_429)],'{}'::bigint[])||ARRAY[$3::bigint] ELSE ustc_speed_runtime.recent_429 END,
   consecutive_429=CASE WHEN $2='success' THEN 0 WHEN $2='rate_limited' THEN ustc_speed_runtime.consecutive_429+1 ELSE ustc_speed_runtime.consecutive_429 END,
   success_streak=CASE WHEN $2='success' THEN ustc_speed_runtime.success_streak+1 ELSE 0 END,
   last_429_at=CASE WHEN $2='rate_limited' THEN $4::timestamptz ELSE ustc_speed_runtime.last_429_at END,last_status=$2,updated_at=NOW()`, s.KeyScope, s.Status, s.FinishedAt.UnixMilli(), s.FinishedAt)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func ustcSpeedWhere(q service.USTCSpeedQuery) (string, []any) {
	args := []any{q.Start, q.End}
	// Real-call speed now comes only from usage records. Older passively
	// captured real samples stay in the table for audit but must not double
	// count the same call after its usage record is imported.
	clauses := []string{"started_at >= $1", "started_at < $2", "(source <> 'real' OR legacy_usage_id IS NOT NULL)"}
	add := func(column string, value any) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf("%s=$%d", column, len(args)))
	}
	if q.Model != "" {
		add("model", q.Model)
	}
	if q.Source != "" && q.Source != "all" {
		add("source", q.Source)
	}
	if q.Status != "" && q.Status != "all" {
		add("status", q.Status)
	}
	if q.Profile != "" {
		if q.Profile == "legacy" && q.GroupBy != "profile" {
			clauses = append(clauses, "payload->>'measurement_mode'='legacy_usage'")
		} else {
			add("profile", q.Profile)
		}
	}
	if q.Reasoning != "" && q.Reasoning != "all" {
		add("reasoning", q.Reasoning)
	}
	if q.Stream == "true" || q.Stream == "false" {
		add("stream", q.Stream == "true")
	}
	if q.TaskID > 0 && q.Source != "real" {
		if q.Source == "probe" {
			add("task_id", q.TaskID)
		} else {
			args = append(args, q.TaskID)
			clauses = append(clauses, fmt.Sprintf("(source='real' OR task_id=$%d)", len(args)))
		}
	}
	if len(q.AccountIDs) > 0 {
		args = append(args, pq.Array(q.AccountIDs))
		clauses = append(clauses, fmt.Sprintf("account_id=ANY($%d)", len(args)))
	}
	for _, filter := range []struct {
		field, known, op string
		value            *int
	}{{"input_tokens", "input_tokens_known", ">=", q.InputMin}, {"input_tokens", "input_tokens_known", "<=", q.InputMax}, {"output_tokens", "token_count_known", ">=", q.OutputMin}, {"output_tokens", "token_count_known", "<=", q.OutputMax}} {
		if filter.value != nil {
			args = append(args, *filter.value)
			clauses = append(clauses, fmt.Sprintf("(COALESCE((payload->>'%s')::boolean,false) AND (payload->>'%s')::bigint %s $%d)", filter.known, filter.field, filter.op, len(args)))
		}
	}
	if f := q.UsageFilters; f != nil {
		for _, field := range []struct {
			name  string
			value int64
		}{{"user_id", f.UserID}, {"api_key_id", f.APIKeyID}, {"account_id", f.AccountID}, {"group_id", f.GroupID}} {
			if field.value > 0 {
				add(field.name, field.value)
			}
		}
		if f.RequestID != "" {
			add("request_id", f.RequestID)
		}
		clauses, args = appendRequestTypeOrStreamWhereCondition(clauses, args, f.RequestType, f.Stream)
		clauses, args = appendNativeCompactionV2WhereCondition(clauses, args, f.NativeCompactionV2, "")
		if f.BillingType != nil {
			add("billing_type", int16(*f.BillingType))
		}
		// The projection contains text-only calls, so null billing_mode means token.
		if f.BillingMode != "" {
			args = append(args, f.BillingMode)
			clauses = append(clauses, fmt.Sprintf("COALESCE(NULLIF(billing_mode,''),'token')=$%d", len(args)))
		}
		if f.UpstreamModelMismatch != nil {
			clauses = append(clauses, upstreamModelMismatchCondition("upstream_model_mismatch", *f.UpstreamModelMismatch))
		}
	}
	return strings.Join(clauses, " AND "), args
}

var ustcSpeedMetrics = map[string]bool{"generation_tps": true, "overall_tps": true, "ttft_ms": true, "first_output_ms": true, "total_ms": true, "output_ms": true, "headers_ms": true, "tail_ms": true, "max_gap_ms": true}

func (r *ustcSpeedRepository) Overview(ctx context.Context, q service.USTCSpeedQuery) (*service.USTCSpeedOverview, error) {
	if !ustcSpeedMetrics[q.Metric] {
		return nil, fmt.Errorf("invalid speed metric")
	}
	groupBy := q.GroupBy
	if groupBy == "" {
		groupBy = "model"
	}
	if groupBy != "model" && groupBy != "profile" {
		return nil, fmt.Errorf("invalid speed group")
	}
	aggregate := q.Aggregate
	if aggregate == "" {
		aggregate = "mean"
	}
	if aggregate != "mean" && aggregate != "p10" && aggregate != "p50" && aggregate != "p90" {
		return nil, fmt.Errorf("invalid speed aggregate")
	}
	where, args := ustcSpeedWhere(q)
	out := &service.USTCSpeedOverview{RangeStart: q.Start, RangeEnd: q.End, Metric: q.Metric, Unit: "ms", Interval: q.Interval, GroupBy: groupBy, Aggregate: aggregate, Series: []service.USTCSpeedPoint{}}
	if strings.HasSuffix(q.Metric, "tps") {
		out.Unit = "tps"
	}
	if q.Analysis {
		var err error
		out.Analysis, err = r.ustcSpeedAnalysis(ctx, q)
		if err != nil {
			return nil, err
		}
	}
	s := &out.Summary
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*),COUNT(*) FILTER(WHERE status='success'),COUNT(*) FILTER(WHERE status='error'),COUNT(*) FILTER(WHERE status='rate_limited'),COUNT(*) FILTER(WHERE status='skipped'),COUNT(*) FILTER(WHERE status='cancelled'),COUNT(*) FILTER(WHERE source='probe'),COUNT(*) FILTER(WHERE source='real'),COALESCE(SUM(COALESCE((payload->>'output_tokens')::bigint,0)),0),MAX(started_at),MAX(started_at) FILTER(WHERE source='real' AND status IN ('success','recorded')),MAX(started_at) FILTER(WHERE source='probe' AND status='success') FROM `+speedObservationTable(q)+` WHERE `+where, args...).Scan(&s.Samples, &s.Successful, &s.Failed, &s.RateLimited, &s.Skipped, &s.Cancelled, &s.ProbeSamples, &s.RealSamples, &s.OutputTokens, &s.LatestAt, &s.LatestRealAt, &s.LatestProbeAt)
	if err != nil {
		return nil, err
	}
	denominator := s.Successful + s.Failed + s.RateLimited
	if denominator > 0 {
		v := float64(s.Successful) * 100 / float64(denominator)
		s.SuccessRate = &v
	}
	interval := q.Interval
	if interval == "auto" || interval == "" {
		interval = service.USTCSpeedAutoInterval(q.End.Sub(q.Start))
	}
	out.Interval = interval
	profileExpr := "profile"
	if groupBy == "model" {
		// Real-call metrics now come from usage records; all of them share one
		// series per model instead of splitting "historical" from "captured".
		profileExpr = "''::text"
	}
	value := speedMetricValue(q.Metric)
	// Failed/cancelled observations remain in status summaries, not healthy-speed quantiles.
	valid := "CASE WHEN status IN ('success','recorded') THEN " + value + " ELSE NULL END"
	if interval == "raw" {
		rows, err := r.db.QueryContext(ctx, `SELECT id,started_at,account_id,account_name,model,source,`+profileExpr+`,round_id,`+valid+` FROM `+speedObservationTable(q)+` WHERE `+where+` ORDER BY started_at DESC,id DESC LIMIT 5001`, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var point service.USTCSpeedPoint
			var id int64
			if err = rows.Scan(&id, &point.Time, &point.AccountID, &point.AccountName, &point.Model, &point.Source, &point.Profile, &point.RoundID, &point.Value); err != nil {
				return nil, err
			}
			point.SampleID = &id
			point.Count = 1
			if point.Value != nil {
				point.ValidCount = 1
			}
			point.Mean = point.Value
			point.P10 = point.Value
			point.P50 = point.Value
			point.P90 = point.Value
			point.Min = point.Value
			point.Max = point.Value
			out.Series = append(out.Series, point)
		}
		if err = rows.Err(); err != nil {
			return nil, err
		}
		if len(out.Series) > 5000 {
			out.Truncated = true
			out.Series = out.Series[:5000]
		}
		for i, j := 0, len(out.Series)-1; i < j; i, j = i+1, j-1 {
			out.Series[i], out.Series[j] = out.Series[j], out.Series[i]
		}
		if q.Hourly {
			if out.Hourly, err = r.ustcSpeedHourly(ctx, q, aggregate, where, args, profileExpr); err != nil {
				return nil, err
			}
		}
		return out, nil
	}
	seconds, ok := service.USTCSpeedIntervalSeconds(interval)
	if !ok {
		return nil, fmt.Errorf("invalid speed interval")
	}
	bucket := fmt.Sprintf("to_timestamp(floor(extract(epoch from started_at)/%d)*%d)", seconds, seconds)
	rows, err := r.db.QueryContext(ctx, `WITH observations AS (SELECT `+bucket+` AS bucket,model,source,`+profileExpr+` AS profile,`+valid+` AS value FROM `+speedObservationTable(q)+` WHERE `+where+`) SELECT bucket,0::bigint,''::text,model,source,profile,COUNT(*),COUNT(value),AVG(value),percentile_cont(0.1) WITHIN GROUP(ORDER BY value),percentile_cont(0.5) WITHIN GROUP(ORDER BY value),percentile_cont(0.9) WITHIN GROUP(ORDER BY value),MIN(value),MAX(value) FROM observations GROUP BY bucket,model,source,profile ORDER BY bucket,model,source,profile LIMIT 5001`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var point service.USTCSpeedPoint
		if err = rows.Scan(&point.Time, &point.AccountID, &point.AccountName, &point.Model, &point.Source, &point.Profile, &point.Count, &point.ValidCount, &point.Mean, &point.P10, &point.P50, &point.P90, &point.Min, &point.Max); err != nil {
			return nil, err
		}
		switch aggregate {
		case "mean":
			point.Value = point.Mean
		case "p10":
			point.Value = point.P10
		case "p90":
			point.Value = point.P90
		case "p50":
			point.Value = point.P50
		}
		out.Series = append(out.Series, point)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(out.Series) > 5000 {
		out.Truncated = true
		out.Series = out.Series[:5000]
	}
	if q.Hourly {
		if out.Hourly, err = r.ustcSpeedHourly(ctx, q, aggregate, where, args, profileExpr); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ustcSpeedHourly aggregates the same filtered calls by local hour of day. The
// caller's IANA zone decides the bucket, so a "best time to run work" answer
// follows the user's clock rather than the database session timezone.
func (r *ustcSpeedRepository) ustcSpeedHourly(ctx context.Context, q service.USTCSpeedQuery, aggregate, where string, args []any, profileExpr string) ([]service.USTCSpeedHourPoint, error) {
	timezone := strings.TrimSpace(q.Timezone)
	if timezone == "" {
		timezone = "UTC"
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		timezone = "UTC"
	}
	hourArgs := append(append([]any(nil), args...), timezone)
	timezoneParam := len(hourArgs)
	// Hourly has its own metric so it can always answer "when is the upstream
	// fastest?" even while the trend chart shows a latency metric.
	metric := q.HourlyMetric
	if metric != "generation_tps" && metric != "overall_tps" {
		metric = q.Metric
	}
	if metric != "generation_tps" && metric != "overall_tps" {
		metric = "overall_tps"
	}
	value := speedMetricValue(metric)
	valid := "CASE WHEN status IN ('success','recorded') THEN " + value + " ELSE NULL END"
	rows, err := r.db.QueryContext(ctx, `WITH observations AS (SELECT EXTRACT(HOUR FROM started_at AT TIME ZONE $`+fmt.Sprint(timezoneParam)+`)::int AS hour,(started_at AT TIME ZONE $`+fmt.Sprint(timezoneParam)+`)::date AS local_day,model,source,`+profileExpr+` AS profile,`+valid+` AS value FROM `+speedObservationTable(q)+` WHERE `+where+`) SELECT hour,model,source,profile,COUNT(*),COUNT(value),COUNT(DISTINCT local_day) FILTER (WHERE value IS NOT NULL),AVG(value),percentile_cont(0.1) WITHIN GROUP(ORDER BY value),percentile_cont(0.5) WITHIN GROUP(ORDER BY value),percentile_cont(0.9) WITHIN GROUP(ORDER BY value),MIN(value),MAX(value) FROM observations GROUP BY hour,model,source,profile ORDER BY hour,model,source,profile LIMIT 2000`, hourArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []service.USTCSpeedHourPoint{}
	for rows.Next() {
		var point service.USTCSpeedHourPoint
		if err = rows.Scan(&point.Hour, &point.Model, &point.Source, &point.Profile, &point.Count, &point.ValidCount, &point.DayCount, &point.Mean, &point.P10, &point.P50, &point.P90, &point.Min, &point.Max); err != nil {
			return nil, err
		}
		switch aggregate {
		case "mean":
			point.Value = point.Mean
		case "p10":
			point.Value = point.P10
		case "p90":
			point.Value = point.P90
		case "p50":
			point.Value = point.P50
		}
		out = append(out, point)
	}
	return out, rows.Err()
}

type ustcSpeedScanner interface{ Scan(...any) error }

const ustcSpeedSampleColumns = "id,legacy_usage_id,task_id,key_scope,started_at,finished_at,payload"

func scanUSTCSpeedSample(row ustcSpeedScanner) (service.USTCSpeedSample, error) {
	var s service.USTCSpeedSample
	var raw []byte
	var id int64
	var legacy, task *int64
	var scope string
	var start, end time.Time
	err := row.Scan(&id, &legacy, &task, &scope, &start, &end, &raw)
	if err != nil {
		return s, err
	}
	if err = json.Unmarshal(raw, &s); err != nil {
		return s, err
	}
	s.ID = id
	s.LegacyUsageID = legacy
	s.TaskID = task
	s.KeyScope = scope
	s.StartedAt = start
	s.FinishedAt = end
	return s, nil
}
func (r *ustcSpeedRepository) ListSamples(ctx context.Context, q service.USTCSpeedQuery) ([]service.USTCSpeedSample, int64, error) {
	where, args := ustcSpeedWhere(q)
	var total int64
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM ustc_speed_samples WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, q.Limit, q.Offset)
	rows, err := r.db.QueryContext(ctx, `SELECT `+ustcSpeedSampleColumns+` FROM ustc_speed_samples WHERE `+where+fmt.Sprintf(` ORDER BY started_at DESC,id DESC LIMIT $%d OFFSET $%d`, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []service.USTCSpeedSample{}
	for rows.Next() {
		s, err := scanUSTCSpeedSample(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, s)
	}
	return out, total, rows.Err()
}
func (r *ustcSpeedRepository) GetSample(ctx context.Context, id int64) (*service.USTCSpeedSample, error) {
	s, err := scanUSTCSpeedSample(r.db.QueryRowContext(ctx, `SELECT `+ustcSpeedSampleColumns+` FROM ustc_speed_samples WHERE id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrUSTCSpeedNotFound
	}
	return &s, err
}
func (r *ustcSpeedRepository) Models(ctx context.Context) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT DISTINCT model FROM ustc_speed_samples WHERE source='probe' AND model<>'' ORDER BY model LIMIT 512`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var model string
		if err = rows.Scan(&model); err != nil {
			return nil, err
		}
		out = append(out, model)
	}
	return out, rows.Err()
}

const ustcSpeedTaskColumns = "id,revision,config,enabled,next_run_at,last_run_at,last_status,created_at,updated_at"

func scanUSTCSpeedTask(row ustcSpeedScanner) (service.USTCSpeedTask, error) {
	var task service.USTCSpeedTask
	var raw []byte
	var id, revision int64
	var enabled bool
	var next, last *time.Time
	var status string
	var created, updated time.Time
	err := row.Scan(&id, &revision, &raw, &enabled, &next, &last, &status, &created, &updated)
	if err != nil {
		return task, err
	}
	if err = json.Unmarshal(raw, &task); err != nil {
		return task, err
	}
	task.ID = id
	task.Revision = revision
	task.Enabled = enabled
	task.NextRunAt = next
	task.LastRunAt = last
	task.LastStatus = status
	task.CreatedAt = created
	task.UpdatedAt = updated
	return task, nil
}
func (r *ustcSpeedRepository) ListTasks(ctx context.Context) ([]service.USTCSpeedTask, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+ustcSpeedTaskColumns+` FROM ustc_speed_tasks ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []service.USTCSpeedTask{}
	for rows.Next() {
		task, err := scanUSTCSpeedTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, task)
	}
	return out, rows.Err()
}
func (r *ustcSpeedRepository) GetTask(ctx context.Context, id int64) (*service.USTCSpeedTask, error) {
	task, err := scanUSTCSpeedTask(r.db.QueryRowContext(ctx, `SELECT `+ustcSpeedTaskColumns+` FROM ustc_speed_tasks WHERE id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrUSTCSpeedNotFound
	}
	return &task, err
}
func (r *ustcSpeedRepository) SaveTask(ctx context.Context, task *service.USTCSpeedTask) error {
	raw, err := json.Marshal(task)
	if err != nil {
		return err
	}
	var saved service.USTCSpeedTask
	next := time.Now().Add(time.Duration(task.IntervalSeconds) * time.Second)
	if task.NextRunAt != nil {
		next = *task.NextRunAt
	}
	if task.ID == 0 {
		saved, err = scanUSTCSpeedTask(r.db.QueryRowContext(ctx, `INSERT INTO ustc_speed_tasks(config,enabled,next_run_at) VALUES($1,$2,$3) RETURNING `+ustcSpeedTaskColumns, raw, task.Enabled, next))
	} else {
		saved, err = scanUSTCSpeedTask(r.db.QueryRowContext(ctx, `UPDATE ustc_speed_tasks SET config=$2,enabled=$3,next_run_at=$4,revision=revision+1,updated_at=NOW() WHERE id=$1 RETURNING `+ustcSpeedTaskColumns, task.ID, raw, task.Enabled, next))
	}
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrUSTCSpeedNotFound
	}
	if err == nil {
		*task = saved
	}
	return err
}
func (r *ustcSpeedRepository) DeleteTask(ctx context.Context, id int64) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM ustc_speed_tasks WHERE id=$1`, id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return service.ErrUSTCSpeedNotFound
	}
	return nil
}
func (r *ustcSpeedRepository) ClaimTask(ctx context.Context, id int64, owner string, ttl time.Duration, force bool, revision int64) (bool, error) {
	result, err := r.db.ExecContext(ctx, `UPDATE ustc_speed_tasks SET lease_owner=$2,lease_until=NOW()+$3*INTERVAL '1 second',lease_revision=revision,last_run_at=NOW(),last_status='running' WHERE id=$1 AND revision=$5 AND(lease_until IS NULL OR lease_until<NOW()) AND($4 OR(enabled AND next_run_at<=NOW()))`, id, owner, ttl.Seconds(), force, revision)
	if err != nil {
		return false, err
	}
	n, _ := result.RowsAffected()
	return n > 0, nil
}
func (r *ustcSpeedRepository) FinishTask(ctx context.Context, id int64, owner string, next time.Time, status, detail string) error {
	// Edits keep the running lease until its owner exits, but a stale run may
	// never replace the schedule saved by the newer task revision.
	_, err := r.db.ExecContext(ctx, `UPDATE ustc_speed_tasks SET lease_owner=NULL,lease_until=NULL,lease_revision=NULL,next_run_at=CASE WHEN lease_revision=revision THEN $3 ELSE next_run_at END,last_status=CASE WHEN lease_revision=revision THEN $4 ELSE 'cancelled' END,config=CASE WHEN lease_revision<>revision THEN config WHEN $5='' THEN config-'last_detail' ELSE jsonb_set(config,'{last_detail}',to_jsonb($5::text),true) END,updated_at=NOW() WHERE id=$1 AND lease_owner=$2`, id, owner, next, status, detail)
	return err
}
func (r *ustcSpeedRepository) AcquireKey(ctx context.Context, scope, owner string, ttl time.Duration, maximum int) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(801760247)`); err != nil {
		return false, err
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM ustc_speed_runtime WHERE lease_until>NOW()`).Scan(&count); err != nil {
		return false, err
	}
	if count >= maximum {
		return false, nil
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO ustc_speed_runtime(key_scope,lease_owner,lease_until) VALUES($1,$2,NOW()+$3*INTERVAL '1 second') ON CONFLICT(key_scope) DO UPDATE SET lease_owner=$2,lease_until=NOW()+$3*INTERVAL '1 second' WHERE ustc_speed_runtime.lease_until IS NULL OR ustc_speed_runtime.lease_until<NOW()`, scope, owner, ttl.Seconds())
	if err != nil {
		return false, err
	}
	n, _ := result.RowsAffected()
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return n > 0, nil
}
func (r *ustcSpeedRepository) ReleaseKey(ctx context.Context, scope, owner string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE ustc_speed_runtime SET lease_owner=NULL,lease_until=NULL WHERE key_scope=$1 AND lease_owner=$2`, scope, owner)
	return err
}
func (r *ustcSpeedRepository) GetRuntime(ctx context.Context, scope string) (*service.USTCSpeedRuntime, error) {
	out := &service.USTCSpeedRuntime{KeyScope: scope}
	var recent pq.Int64Array
	err := r.db.QueryRowContext(ctx, `SELECT recent_429,consecutive_429,success_streak,not_before,last_429_at,paused,lease_until,last_status FROM ustc_speed_runtime WHERE key_scope=$1`, scope).Scan(&recent, &out.Consecutive429, &out.SuccessStreak, &out.NotBefore, &out.Last429At, &out.Paused, &out.LeaseUntil, &out.LastStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	out.Recent429 = []int64(recent)
	return out, err
}
func (r *ustcSpeedRepository) SaveRuntime(ctx context.Context, scope string, value *service.USTCSpeedRuntime) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO ustc_speed_runtime(key_scope,not_before,paused) VALUES($1,$2,$3) ON CONFLICT(key_scope) DO UPDATE SET not_before=$2,paused=$3,updated_at=NOW()`, scope, value.NotBefore, value.Paused)
	return err
}

const ustcSpeedUsageSelect = `SELECT u.*,a.name AS speed_account_name,COALESCE(NULLIF(u.upstream_model,''),u.model) AS speed_model,
	 concat('legacy:',COALESCE(NULLIF(u.upstream_endpoint,''),NULLIF(u.inbound_endpoint,''),'unknown'),
	 ':stream=',u.stream,':effort=',COALESCE(NULLIF(u.reasoning_effort,''),'unknown'),
	 ':in=',CASE WHEN u.input_tokens<128 THEN '0-127' WHEN u.input_tokens<1024 THEN '128-1023' WHEN u.input_tokens<4096 THEN '1k-4k' WHEN u.input_tokens<16384 THEN '4k-16k' WHEN u.input_tokens<65536 THEN '16k-64k' ELSE '64k+' END,
	 ':out=',CASE WHEN u.output_tokens<128 THEN '0-127' WHEN u.output_tokens<1024 THEN '128-1023' WHEN u.output_tokens<4096 THEN '1k-4k' WHEN u.output_tokens<16384 THEN '4k-16k' ELSE '16k+' END,
	 ':cache=',CASE WHEN u.cache_read_tokens>0 THEN 'hit' ELSE 'unknown' END) AS speed_profile
	 FROM usage_logs u JOIN accounts a ON a.id=u.account_id`

const ustcSpeedUsageExclusions = `COALESCE(u.image_count,0)=0 AND COALESCE(u.image_output_tokens,0)=0 AND COALESCE(u.upstream_endpoint,u.inbound_endpoint,'') !~ '/(images|videos|embeddings)(/|$)'`

const ustcSpeedUsageInsert = `INSERT INTO ustc_speed_samples(legacy_usage_id,account_id,account_name,model,source,status,profile,reasoning,stream,started_at,finished_at,payload)
	SELECT u.id,u.account_id,u.speed_account_name,u.speed_model,'real','recorded',u.speed_profile,'unknown',u.stream,u.created_at,u.created_at,
	jsonb_build_object('legacy_usage_id',u.id,'account_id',u.account_id,'account_name',u.speed_account_name,'model',u.speed_model,
	'source','real','status','recorded','profile',u.speed_profile,'reasoning','unknown','stream',u.stream,'measurement_mode','legacy_usage',
	'api_mode',CASE WHEN COALESCE(u.upstream_endpoint,u.inbound_endpoint,'') LIKE '%/responses' THEN 'responses' WHEN COALESCE(u.upstream_endpoint,u.inbound_endpoint,'') LIKE '%/chat/completions' THEN 'chat_completions' ELSE 'unknown' END,
	'input_tokens',u.input_tokens,'input_tokens_known',true,'output_tokens',u.output_tokens,'cached_tokens',u.cache_read_tokens,'cached_tokens_known',u.cache_read_tokens>0,'token_count_known',true,
	'total_ms',CASE WHEN u.duration_ms>=0 THEN u.duration_ms ELSE NULL END,
	'ttft_ms',CASE WHEN u.stream AND u.first_token_ms>=0 THEN u.first_token_ms ELSE NULL END,
	'first_output_ms',CASE WHEN u.stream AND u.first_token_ms>=0 THEN u.first_token_ms ELSE NULL END,
	'output_ms',CASE WHEN u.stream AND u.duration_ms>u.first_token_ms AND u.first_token_ms>=0 THEN u.duration_ms-u.first_token_ms ELSE NULL END,
	'overall_tps',CASE WHEN u.duration_ms>0 AND u.output_tokens>0 THEN u.output_tokens*1000.0/u.duration_ms ELSE NULL END,
	'generation_tps',CASE WHEN u.stream AND u.duration_ms>u.first_token_ms AND u.first_token_ms>=0 AND u.output_tokens>1 THEN u.output_tokens*1000.0/(u.duration_ms-u.first_token_ms) ELSE NULL END)
	FROM %s u ON CONFLICT(legacy_usage_id) DO NOTHING`

// ImportUsage backfills usage records older than the feature's capture start.
func (r *ustcSpeedRepository) ImportUsage(ctx context.Context, before, after time.Time, accountIDs []int64, limit int) (int64, error) {
	if len(accountIDs) == 0 {
		return 0, nil
	}
	// The cursor walks backwards so a large history is imported in bounded pages.
	query := `WITH cursors AS(
	 SELECT account_id,MIN(legacy_usage_id) AS before_id FROM ustc_speed_samples WHERE legacy_usage_id IS NOT NULL GROUP BY account_id
	), historical AS (` + ustcSpeedUsageSelect + ` LEFT JOIN cursors c ON c.account_id=u.account_id
	 WHERE u.account_id=ANY($1) AND u.created_at<$2 AND u.created_at>=$3 AND u.id<COALESCE(c.before_id,9223372036854775807)
	 AND ` + ustcSpeedUsageExclusions + `
	 ORDER BY u.id DESC LIMIT $4
	)
	` + strings.Replace(ustcSpeedUsageInsert, "%s", "historical", 1)
	result, err := r.db.ExecContext(ctx, query, pq.Array(accountIDs), before, after, limit)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// ImportRecentUsage continuously mirrors usage records into speed samples. It
// deduplicates by legacy_usage_id and walks newest-first, so new calls appear
// within one scheduler tick and any gap after an upgrade fills in over time.
func (r *ustcSpeedRepository) ImportRecentUsage(ctx context.Context, before, after time.Time, accountIDs []int64, limit int) (int64, error) {
	if len(accountIDs) == 0 {
		return 0, nil
	}
	query := `WITH recent AS (` + ustcSpeedUsageSelect + `
	 WHERE u.account_id=ANY($1) AND u.created_at<$2 AND u.created_at>=$3
	 AND NOT EXISTS(SELECT 1 FROM ustc_speed_samples s WHERE s.legacy_usage_id=u.id)
	 AND ` + ustcSpeedUsageExclusions + `
	 ORDER BY u.id DESC LIMIT $4
	)
	` + strings.Replace(ustcSpeedUsageInsert, "%s", "recent", 1)
	result, err := r.db.ExecContext(ctx, query, pq.Array(accountIDs), before, after, limit)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
