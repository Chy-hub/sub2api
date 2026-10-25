-- Speed observations share usage history's lifetime. Remove the former
-- retention/budget settings while preserving existing observations and tasks.
UPDATE ustc_speed_config
SET settings = settings - 'retention_days' - 'daily_request_budget' - 'daily_output_token_budget',
    updated_at = NOW()
WHERE id = 1;

UPDATE ustc_speed_tasks
SET config = config - 'max_daily_requests' - 'max_daily_output_tokens',
    updated_at = NOW();
