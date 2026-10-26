package repository

import "github.com/Wei-Shaw/sub2api/internal/service"

const ustcSpeedUsageExclusions = `COALESCE(u.image_count,0)=0 AND COALESCE(u.image_output_tokens,0)=0 AND COALESCE(u.upstream_endpoint,u.inbound_endpoint,'') !~ '/(images|videos|embeddings)(/|$)'`

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
 u.input_tokens,u.output_tokens,u.duration_ms,u.first_token_ms,NULL::jsonb AS payload
 FROM usage_logs u WHERE ` + ustcSpeedUsageExclusions + `) AS speed_observations`
}

// End-to-end rate is also derivable for nonstream benchmarks from their
// original token count and total duration. Do not invent first-token timings.
func speedMetricValue(q service.USTCSpeedQuery, metric string) string {
	// Real calls already have typed timing/token columns. Building a JSON
	// payload here would repeat that conversion inside every aggregate.
	if q.UsageFilters != nil {
		switch metric {
		case "generation_tps":
			return "CASE WHEN stream AND duration_ms>first_token_ms AND first_token_ms>=0 AND output_tokens>=2 THEN output_tokens*1000.0/(duration_ms-first_token_ms) END"
		case "overall_tps":
			return "CASE WHEN duration_ms>0 AND output_tokens>=2 THEN output_tokens*1000.0/duration_ms END"
		case "ttft_ms":
			return "CASE WHEN stream AND first_token_ms>=0 THEN first_token_ms::double precision END"
		default:
			return "NULL::double precision"
		}
	}
	value := "(payload->>'" + metric + "')::double precision"
	if metric == "generation_tps" {
		return "CASE WHEN COALESCE((payload->>'token_count_known')::boolean,false) AND (payload->>'output_tokens')::bigint<2 THEN NULL WHEN payload->>'total_ms' IS NOT NULL AND payload->>'first_output_ms' IS NOT NULL THEN CASE WHEN stream AND COALESCE((payload->>'token_count_known')::boolean,false) AND (payload->>'output_tokens')::bigint>=2 AND (payload->>'first_output_ms')::double precision>=0 AND (payload->>'total_ms')::double precision>(payload->>'first_output_ms')::double precision THEN (payload->>'output_tokens')::double precision*1000/((payload->>'total_ms')::double precision-(payload->>'first_output_ms')::double precision) END ELSE " + value + " END"
	}
	if metric == "overall_tps" {
		return "CASE WHEN COALESCE((payload->>'token_count_known')::boolean,false) AND (payload->>'output_tokens')::bigint<2 THEN NULL ELSE COALESCE(" + value + ",CASE WHEN COALESCE((payload->>'token_count_known')::boolean,false) AND (payload->>'output_tokens')::double precision>=2 AND (payload->>'total_ms')::double precision>0 THEN (payload->>'output_tokens')::double precision*1000/(payload->>'total_ms')::double precision END) END"
	}
	return value
}
