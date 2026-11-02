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

        <div>
          <label class="input-label">{{ t('accountTasks.fieldType') }}</label>
          <select v-model="form.type" class="input">
            <option v-for="type in activeTask?.account_types || []" :key="type" :value="type">
              {{ type }}
            </option>
          </select>
        </div>

        <div>
          <label class="input-label">{{ t('accountTasks.fieldApiKey') }}</label>
          <input
            v-model="form.apiKey"
            type="password"
            class="input font-mono"
            autocomplete="off"
            placeholder="sk-..."
          />
        </div>

        <div v-if="form.type === 'oauth'">
          <label class="input-label">{{ t('accountTasks.fieldAccessToken') }}</label>
          <textarea v-model="form.accessToken" rows="3" class="input font-mono" />
          <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">
            {{ t('accountTasks.oauthHint') }}
          </p>
        </div>

        <div>
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
import { onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import {
  listAccountTasks,
  listMySubmissions,
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
  baseUrl: ''
})

function submittedCount(taskId: number): number {
  return submissions.value.filter(
    (sub) => sub.task_id === taskId && sub.status !== 'rejected'
  ).length
}

function canSubmit(task: AccountTask): boolean {
  return submittedCount(task.id) < task.max_per_user
}

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
  submitError.value = ''
  submitOpen.value = true
}

function closeSubmit(): void {
  if (submitting.value) return
  submitOpen.value = false
}

async function doSubmit(): Promise<void> {
  const task = activeTask.value
  if (!task) return

  const credentials: Record<string, unknown> = {}
  if (form.type === 'apikey') {
    if (!form.apiKey.trim()) {
      submitError.value = t('accountTasks.apiKeyRequired')
      return
    }
    credentials.api_key = form.apiKey.trim()
  } else if (form.type === 'oauth') {
    if (!form.accessToken.trim()) {
      submitError.value = t('accountTasks.accessTokenRequired')
      return
    }
    credentials.access_token = form.accessToken.trim()
  } else {
    if (!form.apiKey.trim()) {
      submitError.value = t('accountTasks.apiKeyRequired')
      return
    }
    credentials.access_token = form.apiKey.trim()
  }
  if (form.baseUrl.trim()) {
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

onMounted(loadAll)
</script>
