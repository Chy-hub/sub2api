<template>
  <AppLayout>
    <div class="space-y-6">
      <div v-if="loading" class="flex justify-center py-12">
        <div
          class="h-8 w-8 animate-spin rounded-full border-2 border-primary-500 border-t-transparent"
        ></div>
      </div>

      <template v-else>
        <!-- 任务列表 -->
        <section class="space-y-3">
          <div class="flex items-center justify-between">
            <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
              {{ t('accountTasks.availableTitle') }}
            </h2>
            <button class="btn btn-secondary btn-sm" :disabled="refreshing" @click="loadAll(true)">
              <Icon name="refresh" size="sm" :class="{ 'animate-spin': refreshing }" />
            </button>
          </div>

          <div v-if="tasks.length === 0" class="card p-10 text-center">
            <div
              class="mx-auto mb-4 flex h-16 w-16 items-center justify-center rounded-full bg-gray-100 dark:bg-dark-700"
            >
              <Icon name="gift" size="xl" class="text-gray-400" />
            </div>
            <h3 class="mb-2 text-base font-semibold text-gray-900 dark:text-white">
              {{ t('accountTasks.emptyTitle') }}
            </h3>
            <p class="text-sm text-gray-500 dark:text-dark-400">
              {{ t('accountTasks.emptyDesc') }}
            </p>
          </div>

          <div v-else class="grid gap-4 lg:grid-cols-2">
            <div
              v-for="task in tasks"
              :key="task.id"
              class="flex flex-col rounded-2xl border border-gray-200 bg-white p-5 dark:border-dark-700 dark:bg-dark-800"
            >
              <div class="flex items-start justify-between gap-3">
                <div class="min-w-0">
                  <div class="flex items-center gap-2">
                    <PlatformIcon :platform="task.platform" size="sm" />
                    <h3 class="truncate font-semibold text-gray-900 dark:text-white">
                      {{ task.name }}
                    </h3>
                  </div>
                  <p class="mt-0.5 text-xs text-gray-400 dark:text-dark-500">
                    {{ platformLabel(task.platform) }}
                  </p>
                </div>
                <span
                  class="shrink-0 rounded-full bg-emerald-100 px-2.5 py-0.5 text-xs font-medium text-emerald-700 dark:bg-emerald-900/40 dark:text-emerald-300"
                >
                  +{{ task.reward_days }} {{ t('accountTasks.days') }}
                </span>
              </div>

              <p
                v-if="task.description"
                class="mt-3 whitespace-pre-wrap text-sm text-gray-600 dark:text-dark-300"
              >
                {{ task.description }}
              </p>

              <dl class="mt-3 space-y-1.5 text-xs text-gray-500 dark:text-dark-400">
                <div class="flex gap-2">
                  <dt class="shrink-0">{{ t('accountTasks.accountTypes') }}</dt>
                  <dd class="flex flex-wrap gap-1">
                    <span
                      v-for="type in task.account_types"
                      :key="type"
                      class="rounded bg-gray-100 px-1.5 py-0.5 font-mono text-[11px] text-gray-700 dark:bg-dark-700 dark:text-dark-200"
                    >
                      {{ type }}
                    </span>
                  </dd>
                </div>
                <div class="flex gap-2">
                  <dt class="shrink-0">{{ t('accountTasks.maxPerUser') }}</dt>
                  <dd>
                    {{ submittedCount(task.id) }} / {{ task.max_per_user }}
                  </dd>
                </div>
                <div v-if="task.verify_models" class="flex gap-2">
                  <dt class="shrink-0">{{ t('accountTasks.verificationLabel') }}</dt>
                  <dd class="text-emerald-600 dark:text-emerald-400">
                    {{ t('accountTasks.verifyEnabled') }}
                  </dd>
                </div>
              </dl>

              <div class="mt-4 flex justify-end">
                <button
                  class="btn btn-primary btn-sm"
                  :disabled="!canSubmit(task)"
                  @click="openSubmit(task)"
                >
                  {{ t('accountTasks.submit') }}
                </button>
              </div>
            </div>
          </div>
        </section>

        <!-- 我的提交 -->
        <section class="space-y-3">
          <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
            {{ t('accountTasks.mySubmissions') }}
          </h2>

          <div v-if="submissions.length === 0" class="card p-8 text-center text-sm text-gray-500 dark:text-dark-400">
            {{ t('accountTasks.noSubmissions') }}
          </div>

          <div v-else class="card overflow-hidden">
            <table class="w-full text-sm">
              <thead class="border-b border-gray-100 bg-gray-50 text-left text-xs uppercase text-gray-500 dark:border-dark-700 dark:bg-dark-900 dark:text-dark-400">
                <tr>
                  <th class="px-4 py-2.5 font-medium">{{ t('accountTasks.colTask') }}</th>
                  <th class="px-4 py-2.5 font-medium">{{ t('accountTasks.colStatus') }}</th>
                  <th class="px-4 py-2.5 font-medium">{{ t('accountTasks.colVerification') }}</th>
                  <th class="px-4 py-2.5 font-medium">{{ t('accountTasks.colReward') }}</th>
                  <th class="px-4 py-2.5 font-medium">{{ t('accountTasks.colCreated') }}</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
                <tr v-for="sub in submissions" :key="sub.id">
                  <td class="px-4 py-2.5 text-gray-900 dark:text-white">
                    {{ taskName(sub.task_id) }}
                    <p
                      v-if="sub.review_note"
                      class="mt-1 max-w-md whitespace-pre-wrap text-xs text-amber-700 dark:text-amber-400"
                    >
                      {{ t('accountTasks.reviewNote') }}: {{ sub.review_note }}
                    </p>
                  </td>
                  <td class="px-4 py-2.5">
                    <span :class="['rounded-full px-2 py-0.5 text-xs font-medium', statusClass(sub.status)]">
                      {{ t(`accountTasks.status.${sub.status}`) }}
                    </span>
                  </td>
                  <td class="px-4 py-2.5 text-xs">
                    <span :class="verificationClass(sub.verification_status)">
                      {{ t(`accountTasks.verification.${sub.verification_status}`) }}
                    </span>
                    <span v-if="sub.model_count > 0" class="ml-1 text-gray-400">
                      ({{ t('accountTasks.modelCount', { count: sub.model_count }) }})
                    </span>
                  </td>
                  <td class="px-4 py-2.5 text-xs">
                    <span v-if="sub.reward_granted_at" class="text-emerald-600 dark:text-emerald-400">
                      {{ t('accountTasks.rewardGranted') }}
                    </span>
                    <span v-else class="text-gray-400">—</span>
                  </td>
                  <td class="px-4 py-2.5 text-xs text-gray-500 dark:text-dark-400">
                    {{ formatDateTime(sub.created_at) }}
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </section>
      </template>
    </div>

    <!-- 提交对话框 -->
    <BaseDialog :show="submitOpen" :title="t('accountTasks.submitTitle')" @close="closeSubmit">
      <form class="space-y-4" @submit.prevent="doSubmit">
        <div>
          <label class="input-label">{{ t('accountTasks.fieldName') }}</label>
          <input v-model="form.name" type="text" class="input" :placeholder="t('accountTasks.fieldNamePlaceholder')" />
        </div>

        <div v-if="(activeTask?.account_types.length || 0) > 1">
          <label class="input-label">{{ t('accountTasks.fieldType') }}</label>
          <select v-model="form.type" class="input">
            <option v-for="type in activeTask?.account_types || []" :key="type" :value="type">
              {{ type }}
            </option>
          </select>
        </div>

        <!-- 密钥类：用户直接粘贴 API Key -->
        <div v-if="submitMode === 'apikey'">
          <label class="input-label">{{ t('accountTasks.fieldApiKey') }}</label>
          <input
            v-model="form.apiKey"
            type="password"
            class="input font-mono"
            autocomplete="off"
            placeholder="sk-..."
          />
        </div>

        <!-- 授权类：跳转到上游完成登录，回来后由服务端换取凭证 -->
        <div v-else class="space-y-3 rounded-lg border border-gray-200 p-4 dark:border-dark-700">
          <p class="text-sm text-gray-600 dark:text-gray-400">
            {{ submitMode === 'reverse'
              ? t('accountTasks.reverseAuthHint')
              : t('accountTasks.oauthHint') }}
          </p>
          <div class="flex flex-wrap items-center gap-2">
            <button
              type="button"
              class="btn btn-primary btn-sm"
              :disabled="authBusy"
              @click="startAuth"
            >
              {{ authURL ? t('accountTasks.restartAuth') : t('accountTasks.startAuth') }}
            </button>
            <a
              v-if="authURL"
              :href="authURL"
              target="_blank"
              rel="noopener noreferrer"
              class="btn btn-secondary btn-sm"
            >
              {{ t('accountTasks.openAuthPage') }}
            </a>
          </div>

          <!-- OAuth 平台授权后会带 code 回跳，需要用户粘贴回来 -->
          <div v-if="submitMode === 'oauth' && authSessionId && !authDone">
            <label class="input-label">{{ t('accountTasks.fieldAuthCode') }}</label>
            <input v-model="form.authCode" type="text" class="input font-mono" autocomplete="off" />
          </div>

          <p v-if="authURL && !authDone" class="text-xs text-amber-600 dark:text-amber-400">
            {{ t('accountTasks.waitingAuth') }}
          </p>
          <p v-if="authDone" class="text-xs text-emerald-600 dark:text-emerald-400">
            {{ t('accountTasks.authSuccess') }}
          </p>
          <p v-if="authError" class="text-xs text-red-600 dark:text-red-400">{{ authError }}</p>
        </div>

        <!-- 任务锁定上游时不给输入框：地址已由任务确定，服务端会自动补全 -->
        <div v-if="submitMode === 'apikey' && lockedBaseURL" class="rounded-lg bg-gray-50 px-3 py-2 text-sm dark:bg-dark-900">
          <span class="text-gray-500 dark:text-dark-400">{{ t('accountTasks.fieldBaseUrl') }}: </span>
          <span class="font-mono text-gray-900 dark:text-white">{{ lockedBaseURL }}</span>
        </div>
        <div v-if="submitMode === 'apikey' && !lockedBaseURL">
          <label class="input-label">{{ t('accountTasks.fieldBaseUrl') }}</label>
          <input
            v-model="form.baseUrl"
            type="text"
            class="input font-mono"
            :placeholder="activeTask?.platform === 'openai' ? 'https://api.openai.com' : ''"
          />
        </div>

        <p
          v-if="submitError"
          class="rounded-lg bg-red-50 px-3 py-2 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300"
        >
          {{ submitError }}
        </p>

        <div class="flex justify-end gap-2 pt-2">
          <button type="button" class="btn btn-secondary" :disabled="submitting" @click="closeSubmit">
            {{ t('common.cancel') }}
          </button>
          <button type="submit" class="btn btn-primary" :disabled="submitting">
            <Icon v-if="submitting" name="refresh" size="sm" class="mr-1 animate-spin" />
            {{ t('accountTasks.confirmSubmit') }}
          </button>
        </div>
      </form>
    </BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import {
  completeSubmissionAuth,
  listAccountTasks,
  listMySubmissions,
  pollSubmissionAuth,
  startSubmissionAuth,
  submitAccount,
  type AccountSubmission,
  type AccountTask
} from '@/api/accountTasks'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import { formatDateTime } from '@/utils/format'
import { platformLabel } from '@/utils/platformColors'

const { t } = useI18n()
const appStore = useAppStore()

const loading = ref(true)
const refreshing = ref(false)
const tasks = ref<AccountTask[]>([])
const submissions = ref<AccountSubmission[]>([])

const submitOpen = ref(false)
const submitting = ref(false)
const submitError = ref('')
const activeTask = ref<AccountTask | null>(null)

const form = reactive({
  name: '',
  type: '',
  apiKey: '',
  accessToken: '',
  baseUrl: '',
  authCode: ''
})

// 授权流程状态
const authBusy = ref(false)
const authURL = ref('')
const authSessionId = ref('')
const authState = ref('')
const authDone = ref(false)
const authError = ref('')
let authPollTimer: ReturnType<typeof setTimeout> | null = null
let authPollGeneration = 0

function submittedCount(taskId: number): number {
  return submissions.value.filter(
    (sub) => sub.task_id === taskId && sub.status !== 'rejected'
  ).length
}

function canSubmit(task: AccountTask): boolean {
  return submittedCount(task.id) < task.max_per_user
}

// 按账号类型决定提交方式：
//   apikey  —— 用户粘贴密钥
//   oauth   —— 浏览器 OAuth 授权后回填 code
//   reverse —— 设备授权登录，服务端轮询拿到登录票据
const submitMode = computed<'apikey' | 'oauth' | 'reverse'>(() => {
  const type = form.type
  if (type === 'reverse') return 'reverse'
  if (type === 'oauth' || type === 'setup-token') return 'oauth'
  return 'apikey'
})

// 任务锁定上游时，地址由任务决定，用户不需要填。
const lockedBaseURL = computed(() => activeTask.value?.expected_base_url ?? '')

function taskName(taskId: number): string {
  return tasks.value.find((task) => task.id === taskId)?.name || `#${taskId}`
}

function statusClass(status: string): string {
  switch (status) {
    case 'approved':
      return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/40 dark:text-emerald-300'
    case 'rejected':
      return 'bg-red-100 text-red-700 dark:bg-red-900/40 dark:text-red-300'
    default:
      return 'bg-amber-100 text-amber-700 dark:bg-amber-900/40 dark:text-amber-300'
  }
}

function verificationClass(status: string): string {
  switch (status) {
    case 'passed':
      return 'text-emerald-600 dark:text-emerald-400'
    case 'failed':
      return 'text-red-600 dark:text-red-400'
    default:
      return 'text-gray-400'
  }
}

async function loadAll(silent = false): Promise<void> {
  if (!silent) {
    loading.value = true
  } else {
    refreshing.value = true
  }
  try {
    const [taskList, submissionList] = await Promise.all([
      listAccountTasks(),
      listMySubmissions()
    ])
    tasks.value = taskList
    submissions.value = submissionList
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('accountTasks.loadFailed')))
  } finally {
    loading.value = false
    refreshing.value = false
  }
}

function openSubmit(task: AccountTask): void {
  activeTask.value = task
  form.name = ''
  form.type = task.account_types[0] || ''
  form.apiKey = ''
  form.accessToken = ''
  form.baseUrl = ''
  form.authCode = ''
  submitError.value = ''
  resetAuthState()
  submitOpen.value = true
}

function resetAuthState(): void {
  stopAuthPolling()
  authBusy.value = false
  authURL.value = ''
  authSessionId.value = ''
  authState.value = ''
  authDone.value = false
  authError.value = ''
}

function stopAuthPolling(): void {
  authPollGeneration += 1
  if (authPollTimer) {
    clearTimeout(authPollTimer)
    authPollTimer = null
  }
}

function closeSubmit(): void {
  if (submitting.value) return
  resetAuthState()
  submitOpen.value = false
}

/** 发起授权：OAuth 平台拿授权链接，反代平台拿设备登录链接。 */
async function startAuth(): Promise<void> {
  const task = activeTask.value
  if (!task) return
  authBusy.value = true
  authError.value = ''
  authDone.value = false
  authURL.value = ''
  authSessionId.value = ''
  try {
    const result = await startSubmissionAuth({
      task_id: task.id,
      redirect_uri: typeof window !== 'undefined' ? window.location.origin + '/account-tasks' : ''
    })
    authURL.value = result.auth_url
    authSessionId.value = result.session_id
    authState.value = result.state || ''
    if (result.mode === 'reverse') {
      scheduleAuthPoll(result.session_id)
    }
  } catch (error) {
    authError.value = extractApiErrorMessage(error, t('accountTasks.authStartFailed'))
  } finally {
    authBusy.value = false
  }
}

/** 反代平台：轮询直到用户在浏览器完成登录。 */
function scheduleAuthPoll(sessionId: string): void {
  stopAuthPolling()
  const generation = authPollGeneration
  const tick = async () => {
    if (generation !== authPollGeneration) return
    try {
      const result = await pollSubmissionAuth(sessionId)
      if (generation !== authPollGeneration) return
      if (result.pending) {
        authPollTimer = setTimeout(tick, 3000)
        return
      }
      authDone.value = true
      if (result.nickname) {
        form.name = form.name || result.nickname
      }
    } catch (error) {
      if (generation !== authPollGeneration) return
      authError.value = extractApiErrorMessage(error, t('accountTasks.authPollFailed'))
    }
  }
  authPollTimer = setTimeout(tick, 2000)
}

async function doSubmit(): Promise<void> {
  const task = activeTask.value
  if (!task) return

  // 授权类账号不走凭证表单，直接提交授权结果。
  if (submitMode.value !== 'apikey') {
    if (!authDone.value) {
      submitError.value = t('accountTasks.authNotCompleted')
      return
    }
    if (submitMode.value === 'oauth') {
      if (!form.authCode.trim()) {
        submitError.value = t('accountTasks.authCodeRequired')
        return
      }
    }
    submitting.value = true
    submitError.value = ''
    try {
      await completeSubmissionAuth({
        task_id: task.id,
        name: form.name.trim() || undefined,
        session_id: authSessionId.value,
        code: form.authCode.trim(),
        state: authState.value
      })
      submitOpen.value = false
      resetAuthState()
      appStore.showSuccess(t('accountTasks.submitSuccess'))
      await loadAll(true)
    } catch (error) {
      submitError.value = extractApiErrorMessage(error, t('accountTasks.submitFailed'))
    } finally {
      submitting.value = false
    }
    return
  }

  const credentials: Record<string, unknown> = {}
  if (!form.apiKey.trim()) {
    submitError.value = t('accountTasks.apiKeyRequired')
    return
  }
  credentials.api_key = form.apiKey.trim()
  // 任务锁定上游时不传，由服务端按任务配置补全，避免用户输入与任务不一致。
  if (!lockedBaseURL.value && form.baseUrl.trim()) {
    credentials.base_url = form.baseUrl.trim()
  }

  submitting.value = true
  submitError.value = ''
  try {
    await submitAccount({
      task_id: task.id,
      name: form.name.trim() || undefined,
      platform: task.platform,
      type: form.type,
      credentials
    })
    submitOpen.value = false
    appStore.showSuccess(t('accountTasks.submitSuccess'))
    await loadAll(true)
  } catch (error) {
    submitError.value = extractApiErrorMessage(error, t('accountTasks.submitFailed'))
  } finally {
    submitting.value = false
  }
}

onBeforeUnmount(stopAuthPolling)

onMounted(loadAll)
</script>
