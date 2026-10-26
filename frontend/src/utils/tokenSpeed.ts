import type { AdminUsageLog } from '@/types'
import { isImageUsage } from '@/utils/billingMode'

export type SpeedRecord = Pick<AdminUsageLog, 'output_tokens' | 'stream' | 'duration_ms' | 'first_token_ms' | 'image_count' | 'billing_mode'>
export function usageOverallTPS(row: SpeedRecord): number | null {
  if (isImageUsage(row) || !Number.isFinite(row.output_tokens) || row.output_tokens < 2 || row.duration_ms == null || !Number.isFinite(row.duration_ms) || row.duration_ms <= 0) return null
  return row.output_tokens * 1000 / row.duration_ms
}
export function usageGenerationTPS(row: SpeedRecord): number | null {
  if (usageOverallTPS(row) === null || !row.stream || row.first_token_ms == null || !Number.isFinite(row.first_token_ms) || row.first_token_ms < 0 || row.duration_ms == null || row.duration_ms <= row.first_token_ms) return null
  return row.output_tokens * 1000 / (row.duration_ms - row.first_token_ms)
}
