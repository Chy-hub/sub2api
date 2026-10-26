package repository

import "github.com/Wei-Shaw/sub2api/internal/service"

// Keep the statistical engine shared while keeping the two data sources
// separate. This read-only projection uses the existing usage fields; it
// neither copies records nor adds measurements to the request path.
func speedObservationTable(q service.USTCSpeedQuery) string {
	if q.UsageFilters == nil {
		return "ustc_speed_samples"
	}
	return `(SELECT u.id,u.id AS legacy_usage_id,NULL::bigint AS task_id,''::text AS round_id,
 u.account_id,''::text AS account_name,COALESCE(NULLIF(TRIM(u.requested_model),''),u.model) AS model,
 'real'::text AS source,'recorded'::text AS status,''::text AS profile,'unknown'::text AS reasoning,
 u.stream,u.created_at AS started_at,u.created_at AS finished_at,
 u.user_id,u.api_key_id,u.group_id,u.request_type,u.openai_ws_mode,u.native_compaction_v2,
 u.billing_type,u.billing_mode,u.upstream_model_mismatch,u.request_id,
 jsonb_build_object('input_tokens',u.input_tokens,'input_tokens_known',true,
 'output_tokens',u.output_tokens,'token_count_known',true,
 'ttft_ms',CASE WHEN u.stream AND u.first_token_ms>=0 THEN u.first_token_ms ELSE NULL END,
 'generation_tps',CASE WHEN u.stream AND u.duration_ms>u.first_token_ms AND u.first_token_ms>=0 AND u.output_tokens>1 THEN u.output_tokens*1000.0/(u.duration_ms-u.first_token_ms) ELSE NULL END,
 'overall_tps',CASE WHEN u.duration_ms>0 AND u.output_tokens>0 THEN u.output_tokens*1000.0/u.duration_ms ELSE NULL END) AS payload
 FROM usage_logs u WHERE ` + ustcSpeedUsageExclusions + `) AS speed_observations`
}

// End-to-end rate is also derivable for nonstream benchmarks from their
// original token count and total duration. Do not invent first-token timings.
func speedMetricValue(metric string) string {
	value := "(payload->>'" + metric + "')::double precision"
	if metric == "overall_tps" {
		return "COALESCE(" + value + ",CASE WHEN COALESCE((payload->>'token_count_known')::boolean,false) AND (payload->>'output_tokens')::double precision>0 AND (payload->>'total_ms')::double precision>0 THEN (payload->>'output_tokens')::double precision*1000/(payload->>'total_ms')::double precision END)"
	}
	return value
}
