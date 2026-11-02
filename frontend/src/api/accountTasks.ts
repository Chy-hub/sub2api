/**
 * 账号贡献任务 API（用户侧）
 * 用户浏览管理员发布的任务，提交自己的上游账号凭证换取订阅奖励。
 */

import { apiClient } from './client'

export type AccountSubmissionStatus = 'pending' | 'approved' | 'rejected'
export type AccountVerificationStatus = 'not_run' | 'passed' | 'failed' | 'unsupported'

/** 面向用户的公开任务视图，不含内部分组与优先级配置 */
export interface AccountTask {
  id: number
  name: string
  description: string
  platform: string
  account_types: string[]
  reward_days: number
  max_per_user: number
  verify_models: boolean
  reward_group_id: number
  /** 非空表示任务已锁定上游，前端应预填并隐藏输入框，服务端会补全 base_url */
  expected_base_url?: string
}

export interface AccountSubmission {
  id: number
  task_id: number
  account_id: number
  status: AccountSubmissionStatus
  verification_status: AccountVerificationStatus
  verification_error?: string
  model_count: number
  review_note?: string
  reward_granted_at?: string
  created_at: string
}

export interface SubmitAccountRequest {
  task_id: number
  name?: string
  platform: string
  type: string
  credentials: Record<string, unknown>
}

/** 获取当前可提交的任务列表 */
export async function listAccountTasks(): Promise<AccountTask[]> {
  const response = await apiClient.get<AccountTask[]>('/account-tasks')
  return response.data
}

/** 获取当前用户的提交记录 */
export async function listMySubmissions(): Promise<AccountSubmission[]> {
  const response = await apiClient.get<AccountSubmission[]>('/account-submissions')
  return response.data
}

/** 提交账号供审核 */
export async function submitAccount(request: SubmitAccountRequest): Promise<AccountSubmission> {
  const response = await apiClient.post<AccountSubmission>('/account-submissions', request)
  return response.data
}
