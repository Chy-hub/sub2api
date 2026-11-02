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

/** 授权方式：apikey 直接填 key，oauth 走浏览器授权，reverse 走设备登录 */
export type SubmissionAuthMode = 'oauth' | 'reverse'

export interface SubmissionAuthStartResult {
  mode: SubmissionAuthMode
  session_id: string
  auth_url: string
  state?: string
  expires_at?: string
}

export interface SubmissionAuthPollResult {
  pending: boolean
  status?: string
  nickname?: string
  /** 反代平台登录完成后的一次性票据，提交时凭它换取凭证 */
  login_ticket?: string
}

export interface SubmissionAuthStartRequest {
  task_id: number
  /** 用户选择的账号类型；Anthropic 用它区分 oauth / setup-token */
  account_type?: string
  oauth_type?: string
}

export interface SubmissionAuthCompleteRequest {
  task_id: number
  account_type?: string
  session_id?: string
  code?: string
  state?: string
  oauth_type?: string
  ticket?: string
  name?: string
}

/** 发起账号授权，复用管理员创建账号时的同一套 OAuth / 反代流程 */
export async function startSubmissionAuth(
  request: SubmissionAuthStartRequest
): Promise<SubmissionAuthStartResult> {
  const response = await apiClient.post<SubmissionAuthStartResult>(
    '/account-submissions/auth/start',
    request
  )
  return response.data
}

/** 轮询设备授权状态（仅反代平台需要） */
export async function pollSubmissionAuth(
  sessionId: string
): Promise<SubmissionAuthPollResult> {
  const response = await apiClient.get<SubmissionAuthPollResult>('/account-submissions/auth/poll', {
    params: { session_id: sessionId }
  })
  return response.data
}

/** 授权完成后登记待审提交 */
export async function completeSubmissionAuth(
  request: SubmissionAuthCompleteRequest
): Promise<AccountSubmission> {
  const response = await apiClient.post<AccountSubmission>(
    '/account-submissions/auth/complete',
    request
  )
  return response.data
}
