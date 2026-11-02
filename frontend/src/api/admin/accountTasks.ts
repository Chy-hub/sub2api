/**
 * 管理端账号贡献任务 API
 * 管理员配置任务、审核用户提交、发放奖励订阅。
 */

import { apiClient } from '../client'
import type { AccountSubmissionStatus, AccountVerificationStatus } from '../accountTasks'

/** 管理端任务视图，包含全部配置项 */
export interface AdminAccountTask {
  id: number
  name: string
  description: string
  enabled: boolean
  platform: string
  account_types: string[]
  target_group_ids: number[]
  reward_group_id: number
  reward_days: number
  max_per_user: number
  expected_base_url: string
  verify_models: boolean
  reject_on_verify_fail: boolean
  priority: number
  concurrency: number
  rate_multiplier?: number | null
  sort_order: number
  created_at: string
  updated_at: string
}

/** 管理端提交视图 */
export interface AdminAccountSubmission {
  id: number
  task_id: number
  user_id: number
  account_id: number
  status: AccountSubmissionStatus
  verification_status: AccountVerificationStatus
  verification_error?: string
  model_count: number
  reviewed_by?: number
  review_note?: string
  reward_subscription_id?: number
  reward_granted_at?: string
  created_at: string
}

export interface AccountTaskPayload {
  name?: string
  description?: string
  enabled?: boolean
  platform?: string
  account_types?: string[]
  target_group_ids?: number[]
  reward_group_id?: number
  reward_days?: number
  max_per_user?: number
  expected_base_url?: string
  verify_models?: boolean
  reject_on_verify_fail?: boolean
  priority?: number
  concurrency?: number
  rate_multiplier?: number | null
  clear_rate_multiplier?: boolean
  sort_order?: number
}

export async function listAccountTasks(): Promise<AdminAccountTask[]> {
  const response = await apiClient.get<AdminAccountTask[]>('/admin/account-tasks')
  return response.data
}

export async function getAccountTask(id: number): Promise<AdminAccountTask> {
  const response = await apiClient.get<AdminAccountTask>(`/admin/account-tasks/${id}`)
  return response.data
}

export async function createAccountTask(payload: AccountTaskPayload): Promise<AdminAccountTask> {
  const response = await apiClient.post<AdminAccountTask>('/admin/account-tasks', payload)
  return response.data
}

export async function updateAccountTask(
  id: number,
  payload: AccountTaskPayload
): Promise<AdminAccountTask> {
  const response = await apiClient.put<AdminAccountTask>(`/admin/account-tasks/${id}`, payload)
  return response.data
}

export async function deleteAccountTask(id: number): Promise<void> {
  await apiClient.delete(`/admin/account-tasks/${id}`)
}

/** 待审提交列表；taskId 为 0 时返回全部待审 */
export async function listPendingSubmissions(taskId?: number): Promise<AdminAccountSubmission[]> {
  const response = await apiClient.get<AdminAccountSubmission[]>('/admin/account-submissions', {
    params: taskId ? { task_id: taskId } : undefined
  })
  return response.data
}

export async function approveSubmission(
  id: number,
  payload: { note?: string; grant_reward?: boolean }
): Promise<AdminAccountSubmission> {
  const response = await apiClient.post<AdminAccountSubmission>(
    `/admin/account-submissions/${id}/approve`,
    payload
  )
  return response.data
}

export async function rejectSubmission(
  id: number,
  payload: { note?: string }
): Promise<AdminAccountSubmission> {
  const response = await apiClient.post<AdminAccountSubmission>(
    `/admin/account-submissions/${id}/reject`,
    payload
  )
  return response.data
}
