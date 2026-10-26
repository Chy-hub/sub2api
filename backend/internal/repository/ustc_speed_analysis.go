package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Analysis deliberately aggregates original rows, independently of the trend
// response's 5,000-point limit. It never averages bucket means or quantiles.
func (r *ustcSpeedRepository) ustcSpeedAnalysis(ctx context.Context, q service.USTCSpeedQuery) (*service.USTCSpeedAnalysis, error) {
	out := service.USTCSpeedAnalysisWindow(q, time.Now())
	all := q
	all.Start, all.End = out.CurrentStart, out.CurrentEnd
	if out.PreviousStart != nil && out.PreviousStart.Before(all.Start) {
		all.Start = *out.PreviousStart
	}
	if out.PreviousEnd != nil && out.PreviousEnd.After(all.End) {
		all.End = *out.PreviousEnd
	}
	where, args := ustcSpeedWhere(all)
	param := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	windows := "('current'::text," + param(out.CurrentStart) + "::timestamptz," + param(out.CurrentEnd) + "::timestamptz)"
	if out.PreviousStart != nil {
		windows += ",('previous'::text," + param(*out.PreviousStart) + "::timestamptz," + param(*out.PreviousEnd) + "::timestamptz)"
	}
	bucketParam := param(out.BucketSeconds)
	// Metric is checked against the fixed allowlist by Overview before use.
	value := speedMetricValue(q, q.Metric)
	stats := `COUNT(*) AS count,COUNT(value) AS valid_count,AVG(value) AS mean,
 percentile_cont(0.1) WITHIN GROUP(ORDER BY value) AS p10,
 percentile_cont(0.5) WITHIN GROUP(ORDER BY value) AS p50,
 percentile_cont(0.9) WITHIN GROUP(ORDER BY value) AS p90,
 stddev_pop(value) AS stddev,MIN(value) AS min,MAX(value) AS max`
	// Benchmark results focus on service speed. Task-length breakdowns belong
	// only to real usage analysis; PostgreSQL skips the unreferenced lengths CTE.
	lengthsJSON := "'[]'::jsonb"
	lengthBuckets := "NULL::int AS input_bucket,NULL::int AS output_bucket"
	if q.UsageFilters != nil {
		lengthsJSON = "COALESCE((SELECT jsonb_agg(to_jsonb(l) ORDER BY model,source,dimension,bucket,period) FROM lengths l),'[]'::jsonb)"
		lengthBuckets = `CASE WHEN input_tokens IS NULL THEN -1
 WHEN input_tokens<1024 THEN 0 WHEN input_tokens<4096 THEN 1
 WHEN input_tokens<16384 THEN 2 WHEN input_tokens<65536 THEN 3 ELSE 4 END AS input_bucket,
 CASE WHEN output_tokens IS NULL THEN -1
 WHEN output_tokens<128 THEN 0 WHEN output_tokens<512 THEN 1
 WHEN output_tokens<2048 THEN 2 WHEN output_tokens<8192 THEN 3 ELSE 4 END AS output_bucket`
	}
	query := `WITH windows(period,from_at,to_at) AS (VALUES ` + windows + `),
 observations AS MATERIALIZED (
 SELECT period,model,source,
 floor(extract(epoch FROM started_at-from_at)/` + bucketParam + `)::int AS bucket,
 CASE WHEN status IN ('success','recorded') AND ` + value + `>=0 AND ` + value + `<'Infinity'::float8 THEN ` + value + ` END AS value,
 ` + lengthBuckets + `
 FROM ` + speedObservationTable(q) + ` JOIN windows ON started_at>=from_at AND started_at<to_at WHERE ` + where + `),
 groups AS (SELECT period,model,source,` + stats + ` FROM observations GROUP BY period,model,source),
 bounds AS (SELECT model,source,MIN(value) AS lo,MAX(value) AS hi FROM observations WHERE value IS NOT NULL GROUP BY model,source),
 binned AS (SELECT o.period,o.model,o.source,lo,hi,
 CASE WHEN hi=lo THEN 0 ELSE LEAST(15,GREATEST(0,width_bucket(value,lo,hi,16)-1)) END AS bin
 FROM observations o JOIN bounds b USING(model,source) WHERE value IS NOT NULL),
 distribution AS (SELECT period,model,source,bin,lo+(hi-lo)*bin/16 AS lower,
 lo+(hi-lo)*(bin+1)/16 AS upper,COUNT(*) AS count FROM binned GROUP BY period,model,source,bin,lo,hi),
 lengths AS (SELECT period,model,source,dimension,length_bucket AS bucket,` + stats + `
 FROM observations CROSS JOIN LATERAL (VALUES ('input',input_bucket),('output',output_bucket)) AS d(dimension,length_bucket)
 GROUP BY period,model,source,dimension,length_bucket),
 trend AS (SELECT period,model,source,bucket,` + stats + ` FROM observations GROUP BY period,model,source,bucket)
 SELECT jsonb_build_object(
 'groups',COALESCE((SELECT jsonb_agg(to_jsonb(g) ORDER BY model,source,period) FROM groups g),'[]'::jsonb),
 'distribution',COALESCE((SELECT jsonb_agg(to_jsonb(d) ORDER BY model,source,period,bin) FROM distribution d),'[]'::jsonb),
 'lengths',` + lengthsJSON + `,
 'trend',COALESCE((SELECT jsonb_agg(to_jsonb(t) ORDER BY model,source,period,bucket) FROM trend t),'[]'::jsonb))`
	var raw []byte
	if err := r.db.QueryRowContext(ctx, query, args...).Scan(&raw); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
