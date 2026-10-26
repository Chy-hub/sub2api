/** Admin API for USTC latency and throughput observations. */

import { apiClient } from '../client'

export type USTCSpeedSource = 'real' | 'probe' | 'all'
export type USTCSpeedMetric =
  | 'generation_tps'
  | 'overall_tps'
  | 'ttft_ms'
  | 'first_output_ms'
  | 'total_ms'
  | 'output_ms'
  | 'headers_ms'
  | 'tail_ms'
  | 'max_gap_ms'
export type USTCSpeedInterval = 'raw' | '1m' | '5m' | '15m' | '1h' | '1d' | 'auto'
export type USTCSpeedAggregate = 'p50' | 'mean' | 'p10' | 'p90'

export interface USTCSpeedAccountDimension {
  id: number
  key_account_id?: number
  name: string
  models: string[]
  capacity?: Record<string, unknown>
}

export interface USTCSpeedDimensions {
  accounts: USTCSpeedAccountDimension[]
  models: string[]
}

export interface USTCSpeedQuery {
  start_date: string
  end_date: string
  timezone: string
  model?: string
  source?: USTCSpeedSource
  account_ids?: string
  task_id?: number
  reasoning?: 'all' | 'on' | 'off' | 'unknown'
  stream?: 'all' | 'true' | 'false'
  status?: string
  profile?: string
  input_min?: number
  input_max?: number
  output_min?: number
  output_max?: number
  metric?: USTCSpeedMetric
  interval?: USTCSpeedInterval
  aggregate?: USTCSpeedAggregate
  group_by?: 'model' | 'profile'
  limit?: number
  offset?: number
  hourly?: boolean
  hourly_metric?: 'generation_tps' | 'overall_tps'
  last_hours?: number
}

export interface USTCSpeedObservation {
  headers_ms: number | null
  total_ms: number | null
  first_output_ms: number | null
  ttft_ms: number | null
  output_ms: number | null
  tail_ms: number | null
  generation_tps: number | null
  overall_tps: number | null
  max_gap_ms: number | null
  input_tokens: number
  input_tokens_known?: boolean
  output_tokens: number
  reasoning_tokens: number
  cached_tokens: number
  reasoning_tokens_known?: boolean
  cached_tokens_known?: boolean
  token_count_known: boolean
  output_chars: number
  reasoning_chars: number
  event_count: number
  gap_over_1s: number
  gap_over_2s: number
  finish_reason: string
  observed_model: string
  error_code?: string
  stream_done: boolean
  provider_cost: number | null
  measurement_mode: string
}

export interface USTCSpeedSample extends USTCSpeedObservation {
  id: number
  task_id: number | null
  round_id: string
  account_id: number
  account_name: string
  model: string
  source: string
  status: string
  reason?: string
  http_status: number
  stream: boolean
  api_mode: string
  profile: string
  reasoning: string
  started_at: string
  finished_at: string
}

export interface USTCSpeedPoint {
  time: string
  account_id: number
  account_name: string
  model: string
  source: string
  profile: string
  round_id: string
  sample_id?: number
  count: number
  valid_count: number
  value: number | null
  mean: number | null
  p10: number | null
  p50: number | null
  p90: number | null
  min: number | null
  max: number | null
}

export interface USTCSpeedHourPoint {
  hour: number
  model: string
  source: string
  profile: string
  count: number
  valid_count: number
  day_count: number
  value: number | null
  mean: number | null
  p10: number | null
  p50: number | null
  p90: number | null
  min: number | null
  max: number | null
}

export interface USTCSpeedSummary {
  samples: number
  successful: number
  failed: number
  rate_limited: number
  skipped: number
  cancelled: number
  probe_samples: number
  real_samples: number
  output_tokens: number
  success_rate: number | null
  latest_at: string | null
  latest_real_at: string | null
  latest_probe_at: string | null
}

export interface USTCSpeedOverview {
  range_start: string
  range_end: string
  group_by: 'model' | 'profile'
  metric: USTCSpeedMetric
  unit: string
  interval: USTCSpeedInterval
  aggregate: USTCSpeedAggregate
  requested_source: USTCSpeedSource
  fallback_applied: boolean
  fallback_window_seconds: number
  summary: USTCSpeedSummary
  series: USTCSpeedPoint[]
  hourly?: USTCSpeedHourPoint[]
  truncated: boolean
  dropped_samples: number
}

export interface USTCSpeedTask {
  id: number
  name: string
  enabled: boolean
  all_accounts: boolean
  account_ids: number[]
  models: string[]
  interval_seconds: number
  jitter_seconds: number
  cron_expression: string
  timezone: string
  active_start: string
  active_end: string
  days_of_week: number[]
  api_mode: string
  stream: boolean
  prompt: string
  prompt_variants: string[]
  max_tokens: number
  temperature: number
  top_p: number | null
  seed: number | null
  stop: string[]
  thinking_mode: string
  reasoning_effort: string
  timeout_seconds: number
  first_output_timeout_seconds: number
  max_parallel_accounts: number
  max_start_skew_seconds: number
  reserve_rpm: number
  reserve_concurrency: number
  max_active_requests: number
  skip_if_user_waiting: boolean
  backoff_after_429: number
  backoff_window_seconds: number
  backoff_multiplier: number
  max_interval_seconds: number
  pause_after_429: number
  recovery_successes: number
  real_fallback: boolean
  real_fallback_max_age_seconds: number
  real_fallback_min_samples: number
  next_run_at: string | null
  last_run_at: string | null
  last_status: string
  last_detail?: string
  created_at: string
  updated_at: string
}

export type USTCSpeedTaskInput = Omit<
  USTCSpeedTask,
  'next_run_at' | 'last_run_at' | 'last_status' | 'last_detail' | 'created_at' | 'updated_at'
>

export interface USTCSpeedSettings {
  capture_enabled: boolean
  max_background_concurrency: number
}

export interface USTCSpeedRuntime {
  account_id: number
  account_name: string
  consecutive_429: number
  success_streak: number
  not_before: string | null
  last_429_at: string | null
  paused: boolean
  lease_until: string | null
  last_status: string
}

export interface USTCSpeedSampleList {
  items: USTCSpeedSample[]
  total: number
}

const basePath = '/admin/ustc-speed'

export async function getDimensions(): Promise<USTCSpeedDimensions> {
  const { data } = await apiClient.get<USTCSpeedDimensions>(`${basePath}/dimensions`)
  return data
}

export async function getOverview(params: USTCSpeedQuery, options?: { signal?: AbortSignal }): Promise<USTCSpeedOverview> {
  const { data } = await apiClient.get<USTCSpeedOverview>(`${basePath}/overview`, { params, signal: options?.signal })
  return data
}

export async function listSamples(params: USTCSpeedQuery, options?: { signal?: AbortSignal }): Promise<USTCSpeedSampleList> {
  const { data } = await apiClient.get<USTCSpeedSampleList>(`${basePath}/samples`, { params, signal: options?.signal })
  return data
}

export async function getSample(id: number): Promise<USTCSpeedSample> {
  const { data } = await apiClient.get<USTCSpeedSample>(`${basePath}/samples/${id}`)
  return data
}

export async function listTasks(): Promise<{ items: USTCSpeedTask[] }> {
  const { data } = await apiClient.get<{ items: USTCSpeedTask[] }>(`${basePath}/tasks`)
  return data
}

export async function createTask(task: USTCSpeedTaskInput): Promise<USTCSpeedTask> {
  const { data } = await apiClient.post<USTCSpeedTask>(`${basePath}/tasks`, task)
  return data
}

export async function updateTask(id: number, task: USTCSpeedTaskInput): Promise<USTCSpeedTask> {
  const { data } = await apiClient.put<USTCSpeedTask>(`${basePath}/tasks/${id}`, task)
  return data
}

export async function deleteTask(id: number): Promise<void> {
  await apiClient.delete(`${basePath}/tasks/${id}`)
}

export async function runTask(id: number): Promise<{ round_id: string; status: 'queued' | string }> {
  const { data } = await apiClient.post<{ round_id: string; status: 'queued' | string }>(`${basePath}/tasks/${id}/run`)
  return data
}

export async function getSettings(): Promise<USTCSpeedSettings> {
  const { data } = await apiClient.get<USTCSpeedSettings>(`${basePath}/settings`)
  return data
}

export async function saveSettings(settings: USTCSpeedSettings): Promise<USTCSpeedSettings> {
  const { data } = await apiClient.put<USTCSpeedSettings>(`${basePath}/settings`, settings)
  return data
}

export async function getRuntime(): Promise<{ items: USTCSpeedRuntime[] }> {
  const { data } = await apiClient.get<{ items: USTCSpeedRuntime[] }>(`${basePath}/runtime`)
  return data
}

export const adminUSTCSpeedAPI = {
  getDimensions,
  getOverview,
  listSamples,
  getSample,
  listTasks,
  createTask,
  updateTask,
  deleteTask,
  runTask,
  getSettings,
  saveSettings,
  getRuntime
}

export default adminUSTCSpeedAPI
