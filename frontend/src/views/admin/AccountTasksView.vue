<template>
  <AppLayout>
    <div class="space-y-6">
      <!-- 任务列表 -->
      <section class="space-y-3">
        <div class="flex items-center justify-between">
          <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
            {{ t('admin.accountTasks.taskList') }}
          </h2>
          <button class="btn btn-primary btn-sm" @click="openCreate">
            <Icon name="plus" size="sm" class="mr-1" />
            {{ t('admin.accountTasks.createTask') }}
          </button>
        </div>

        <div v-if="loading" class="flex justify-center py-10">
          <div class="h-8 w-8 animate-spin rounded-full border-2 border-primary-500 border-t-transparent"></div>
        </div>

        <div v-else-if="tasks.length === 0" class="card p-10 text-center text-sm text-gray-500 dark:text-dark-400">
          {{ t('admin.accountTasks.noTasks') }}
        </div>

        <div v-else class="card overflow-x-auto">
          <table class="w-full text-sm">
            <thead class="border-b border-gray-100 bg-gray-50 text-left text-xs uppercase text-gray-500 dark:border-dark-700 dark:bg-dark-900 dark:text-dark-400">
              <tr>
                <th class="px-4 py-2.5 font-medium">{{ t('admin.accountTasks.colName') }}</th>
                <th class="px-4 py-2.5 font-medium">{{ t('admin.accountTasks.colPlatform') }}</th>
                <th class="px-4 py-2.5 font-medium">{{ t('admin.accountTasks.colTypes') }}</th>
                <th class="px-4 py-2.5 font-medium">{{ t('admin.accountTasks.colReward') }}</th>
                <th class="px-4 py-2.5 font-medium">{{ t('admin.accountTasks.colTargetGroups') }}</th>
                <th class="px-4 py-2.5 font-medium">{{ t('admin.accountTasks.colEnabled') }}</th>
                <th class="px-4 py-2.5"></th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
              <tr v-for="task in tasks" :key="task.id" :class="{ 'opacity-50': !task.enabled }">
                <td class="px-4 py-2.5">
                  <div class="font-medium text-gray-900 dark:text-white">{{ task.name }}</div>
                  <div v-if="task.description" class="mt-0.5 max-w-xs truncate text-xs text-gray-400">
                    {{ task.description }}
                  </div>
                </td>
                <td class="px-4 py-2.5">{{ platformLabel(task.platform) }}</td>
                <td class="px-4 py-2.5 text-xs">
                  <span
                    v-for="type in task.account_types"
                    :key="type"
                    class="mr-1 rounded bg-gray-100 px-1.5 py-0.5 font-mono dark:bg-dark-700"
                  >
                    {{ type }}
                  </span>
                </td>
                <td class="px-4 py-2.5 text-xs">
                  {{ groupName(task.reward_group_id) }} · {{ task.reward_days }} {{ t('accountTasks.days') }}
                </td>
                <td class="px-4 py-2.5 text-xs">
                  <span v-if="task.target_group_ids.length === 0" class="text-amber-600 dark:text-amber-400">
                    {{ t('admin.accountTasks.ungroupedHint') }}
                  </span>
                  <span v-else>
                    {{ task.target_group_ids.map(groupName).join(', ') }}
                  </span>
                </td>
                <td class="px-4 py-2.5">
                  <span
                    :class="[
                      'rounded-full px-2 py-0.5 text-xs font-medium',
                      task.enabled
                        ? 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/40 dark:text-emerald-300'
                        : 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-400'
                    ]"
                  >
                    {{ task.enabled ? t('common.enabled') : t('common.disabled') }}
                  </span>
                </td>
                <td class="px-4 py-2.5 text-right">
                  <button class="btn btn-secondary btn-xs mr-1" @click="openEdit(task)">
                    {{ t('common.edit') }}
                  </button>
                  <button class="btn btn-secondary btn-xs text-red-600" @click="confirmDelete(task)">
                    {{ t('common.delete') }}
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <!-- 待审提交 -->
      <section class="space-y-3">
        <div class="flex items-center justify-between">
          <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
            {{ t('admin.accountTasks.pendingSubmissions') }}
          </h2>
          <button class="btn btn-secondary btn-sm" :disabled="loadingSubmissions" @click="loadSubmissions">
            <Icon name="refresh" size="sm" :class="{ 'animate-spin': loadingSubmissions }" />
          </button>
        </div>

        <div v-if="!loadingSubmissions && submissions.length === 0" class="card p-8 text-center text-sm text-gray-500 dark:text-dark-400">
          {{ t('admin.accountTasks.noPending') }}
        </div>

        <div v-else class="card overflow-x-auto">
          <table class="w-full text-sm">
            <thead class="border-b border-gray-100 bg-gray-50 text-left text-xs uppercase text-gray-500 dark:border-dark-700 dark:bg-dark-900 dark:text-dark-400">
              <tr>
                <th class="px-4 py-2.5 font-medium">{{ t('admin.accountTasks.colSubmission') }}</th>
                <th class="px-4 py-2.5 font-medium">{{ t('admin.accountTasks.colTask') }}</th>
                <th class="px-4 py-2.5 font-medium">{{ t('admin.accountTasks.colVerification') }}</th>
                <th class="px-4 py-2.5 font-medium">{{ t('admin.accountTasks.colAccount') }}</th>
                <th class="px-4 py-2.5 font-medium">{{ t('admin.accountTasks.colCreated') }}</th>
                <th class="px-4 py-2.5"></th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
              <tr v-for="sub in submissions" :key="sub.id">
                <td class="px-4 py-2.5 font-mono text-xs">#{{ sub.id }}</td>
                <td class="px-4 py-2.5">{{ taskName(sub.task_id) }}</td>
                <td class="px-4 py-2.5 text-xs">
                  <span :class="verificationClass(sub.verification_status)">
                    {{ t(`accountTasks.verification.${sub.verification_status}`) }}
                  </span>
                  <span v-if="sub.model_count > 0" class="ml-1 text-gray-400">
                    ({{ t('accountTasks.modelCount', { count: sub.model_count }) }})
                  </span>
                  <div v-if="sub.verification_error" class="mt-0.5 max-w-xs truncate text-red-500" :title="sub.verification_error">
                    {{ sub.verification_error }}
                  </div>
                </td>
                <td class="px-4 py-2.5">
                  <RouterLink
                    v-if="sub.account_id"
                    :to="`/admin/accounts?search=${sub.account_id}`"
                    class="text-primary-600 hover:underline dark:text-primary-400"
                  >
                    #{{ sub.account_id }}
                  </RouterLink>
                  <button
                    v-if="sub.account_id"
                    class="ml-2 text-xs text-gray-500 hover:text-gray-700 dark:hover:text-gray-300"
                    @click="testAccount(sub.account_id)"
                  >
                    {{ t('admin.accountTasks.testAccount') }}
                  </button>
                </td>
                <td class="px-4 py-2.5 text-xs text-gray-500 dark:text-dark-400">
                  {{ formatDateTime(sub.created_at) }}
                </td>
                <td class="px-4 py-2.5 text-right whitespace-nowrap">
                  <button
                    class="btn btn-primary btn-xs mr-1"
                    :disabled="reviewingId === sub.id"
                    @click="approve(sub)"
                  >
                    {{ t('admin.accountTasks.approve') }}
                  </button>
                  <button
                    class="btn btn-secondary btn-xs text-red-600"
                    :disabled="reviewingId === sub.id"
                    @click="reject(sub)"
                  >
                    {{ t('admin.accountTasks.reject') }}
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </div>

    <!-- 任务编辑对话框 -->
    <BaseDialog :show="editorOpen" :title="editingId ? t('admin.accountTasks.editTask') : t('admin.accountTasks.createTask')" width="wide" @close="closeEditor">
      <form class="space-y-4" @submit.prevent="saveTask">
        <div class="grid gap-4 sm:grid-cols-2">
          <div>
            <label class="input-label">{{ t('admin.accountTasks.fieldName') }} *</label>
            <input v-model="form.name" type="text" class="input" required />
          </div>
          <div>
            <label class="input-label">{{ t('admin.accountTasks.fieldPlatform') }} *</label>
            <select v-model="form.platform" class="input" required>
              <option v-for="option in platformOptions" :key="option.value" :value="option.value">
                {{ option.label }}
              </option>
            </select>
          </div>
        </div>

        <div>
          <label class="input-label">{{ t('admin.accountTasks.fieldDescription') }}</label>
          <textarea v-model="form.description" rows="2" class="input" />
        </div>

        <div>
          <label class="input-label">{{ t('admin.accountTasks.fieldTypes') }} *</label>
          <div class="flex flex-wrap gap-3">
            <label v-for="option in allowedAccountTypes" :key="option" class="flex items-center gap-1.5 text-sm">
              <input v-model="form.accountTypes" type="checkbox" :value="option" />
              <span class="font-mono text-xs">{{ option }}</span>
            </label>
          </div>
          <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">
            {{ t('admin.accountTasks.fieldTypesHint') }}
          </p>
        </div>

        <div class="grid gap-4 sm:grid-cols-2">
          <div>
            <label class="input-label">{{ t('admin.accountTasks.fieldRewardGroup') }} *</label>
            <select v-model.number="form.rewardGroupId" class="input" required>
              <option :value="0" disabled>{{ t('admin.accountTasks.selectPlaceholder') }}</option>
              <option v-for="group in subscriptionGroups" :key="group.id" :value="group.id">
                {{ group.name }}
              </option>
            </select>
            <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">
              {{ t('admin.accountTasks.fieldRewardGroupHint') }}
            </p>
          </div>
          <div>
            <label class="input-label">{{ t('admin.accountTasks.fieldRewardDays') }} *</label>
            <input v-model.number="form.rewardDays" type="number" min="1" class="input" required />
          </div>
        </div>

        <div>
          <label class="input-label">{{ t('admin.accountTasks.fieldTargetGroups') }}</label>
          <GroupSelector v-model="form.targetGroupIds" :groups="formGroups" :platform="form.platform as GroupPlatform" />
          <p class="mt-1 text-xs text-amber-600 dark:text-amber-400">
            {{ t('admin.accountTasks.fieldTargetGroupsHint') }}
          </p>
        </div>

        <div class="grid gap-4 sm:grid-cols-2">
          <!-- 上游地址只对「填密钥」类账号有意义：OAuth 与反代平台的地址由
               平台适配器决定，用户无从填写，这里也不该出现。 -->
          <div v-if="supportsCustomBaseURL">
            <label class="input-label">{{ t('admin.accountTasks.fieldExpectedBaseUrl') }}</label>
            <input
              v-model="form.expectedBaseUrl"
              type="text"
              class="input font-mono"
              placeholder="api.llm.ustc.edu.cn"
            />
            <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">
              {{ t('admin.accountTasks.fieldExpectedBaseUrlHint') }}
            </p>
          </div>
          <div>
            <label class="input-label">{{ t('admin.accountTasks.fieldMaxPerUser') }}</label>
            <input v-model.number="form.maxPerUser" type="number" min="1" class="input" />
          </div>
        </div>

        <div class="grid gap-4 sm:grid-cols-3">
          <div>
            <label class="input-label">{{ t('admin.accountTasks.fieldPriority') }}</label>
            <input v-model.number="form.priority" type="number" min="1" class="input" />
          </div>
          <div>
            <label class="input-label">{{ t('admin.accountTasks.fieldConcurrency') }}</label>
            <input v-model.number="form.concurrency" type="number" min="1" class="input" />
          </div>
          <div>
            <label class="input-label">{{ t('admin.accountTasks.fieldSortOrder') }}</label>
            <input v-model.number="form.sortOrder" type="number" class="input" />
          </div>
        </div>

        <div>
          <label class="input-label">{{ t('admin.accountTasks.fieldRateMultiplier') }}</label>
          <input v-model="form.rateMultiplier" type="number" min="0" step="0.0001" class="input" :placeholder="t('admin.accountTasks.fieldRateMultiplierPlaceholder')" />
          <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">
            {{ t('admin.accountTasks.fieldRateMultiplierHint') }}
          </p>
        </div>

        <div class="space-y-2 rounded-lg bg-gray-50 p-3 dark:bg-dark-900">
          <label class="flex items-center gap-2 text-sm">
            <input v-model="form.verifyModels" type="checkbox" />
            <span>{{ t('admin.accountTasks.fieldVerifyModels') }}</span>
          </label>
          <p class="pl-6 text-xs text-gray-500 dark:text-dark-400">
            {{ t('admin.accountTasks.fieldVerifyModelsHint') }}
          </p>
          <label v-if="form.verifyModels" class="flex items-center gap-2 pl-6 text-sm">
            <input v-model="form.rejectOnVerifyFail" type="checkbox" />
            <span>{{ t('admin.accountTasks.fieldRejectOnVerifyFail') }}</span>
          </label>
          <label class="flex items-center gap-2 text-sm">
            <input v-model="form.enabled" type="checkbox" />
            <span>{{ t('admin.accountTasks.fieldEnabled') }}</span>
          </label>
        </div>

        <p v-if="formError" class="rounded-lg bg-red-50 px-3 py-2 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300">
          {{ formError }}
        </p>

        <div class="flex justify-end gap-2 pt-2">
          <button type="button" class="btn btn-secondary" :disabled="saving" @click="closeEditor">
            {{ t('common.cancel') }}
          </button>
          <button type="submit" class="btn btn-primary" :disabled="saving">
            {{ t('common.save') }}
          </button>
        </div>
      </form>
    </BaseDialog>

    <!-- 审核确认对话框 -->
    <BaseDialog :show="reviewOpen" :title="reviewTitle" @close="closeReview">
      <div class="space-y-4">
        <div>
          <label class="input-label">{{ t('admin.accountTasks.reviewNote') }}</label>
          <textarea v-model="reviewNote" rows="3" class="input" />
        </div>
        <label v-if="reviewAction === 'approve'" class="flex items-center gap-2 text-sm">
          <input v-model="reviewGrantReward" type="checkbox" />
          <span>{{ t('admin.accountTasks.grantReward') }}</span>
        </label>
        <div class="flex justify-end gap-2">
          <button class="btn btn-secondary" @click="closeReview">{{ t('common.cancel') }}</button>
          <button class="btn btn-primary" :disabled="reviewingId !== null" @click="submitReview">
            {{ t('common.confirm') }}
          </button>
        </div>
      </div>
    </BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, reactive, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import GroupSelector from '@/components/common/GroupSelector.vue'
import Icon from '@/components/icons/Icon.vue'
import adminGroupAPI from '@/api/admin/groups'
import {
  approveSubmission,
  createAccountTask,
  deleteAccountTask,
  listAccountTasks,
  listPendingSubmissions,
  rejectSubmission,
  updateAccountTask,
  type AccountTaskPayload,
  type AdminAccountSubmission,
  type AdminAccountTask
} from '@/api/admin/accountTasks'
import { CONCRETE_PLATFORM_OPTIONS } from '@/constants/platforms'
import { accountTypesForPlatform } from '@/constants/platformCatalog'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import { formatDateTime } from '@/utils/format'
import { platformLabel } from '@/utils/platformColors'
import type { AdminGroup, GroupPlatform } from '@/types'

const { t } = useI18n()
const appStore = useAppStore()

const loading = ref(true)
const loadingSubmissions = ref(false)
const tasks = ref<AdminAccountTask[]>([])
const submissions = ref<AdminAccountSubmission[]>([])
const groups = ref<AdminGroup[]>([])

const platformOptions = computed(() => CONCRETE_PLATFORM_OPTIONS)

const groupMap = computed(() => {
  const map = new Map<number, string>()
  for (const group of groups.value) {
    map.set(group.id, group.name)
  }
  return map
})

function groupName(id: number): string {
  return groupMap.value.get(id) ?? `#${id}`
}

/** 奖励只能发订阅型分组，这里只列可选项，避免管理员选到标准分组。 */
const subscriptionGroups = computed(() =>
  groups.value.filter((group) => group.subscription_type === 'subscription')
)

/** 账号入组候选：与任务平台一致的活跃分组。 */
const formGroups = computed(() =>
  groups.value.filter((group) => group.platform === form.platform)
)

function taskName(taskId: number): string {
  return tasks.value.find((task) => task.id === taskId)?.name || `#${taskId}`
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

// ---------- 任务编辑 ----------

const editorOpen = ref(false)
const editingId = ref<number | null>(null)
const saving = ref(false)
const formError = ref('')

const form = reactive({
  name: '',
  description: '',
  platform: 'openai',
  accountTypes: ['apikey'] as string[],
  rewardGroupId: 0,
  rewardDays: 30,
  targetGroupIds: [] as number[],
  expectedBaseUrl: '',
  maxPerUser: 1,
  priority: 50,
  concurrency: 3,
  sortOrder: 0,
  rateMultiplier: '',
  verifyModels: true,
  rejectOnVerifyFail: true,
  enabled: true
})

// 账号类型选项按平台推导：反代平台只有 reverse，TypeSafe 只有 apikey。
// 写死全量列表会让管理员配出必然创建失败的任务。
const allowedAccountTypes = computed(() => accountTypesForPlatform(form.platform))

// 只有「填密钥」类账号才能自定义上游地址。OAuth 平台的地址由平台适配器固定，
// 反代平台（Qoder / WorkBuddy）与服务账号/Bedrock 更没有这个概念。
const supportsCustomBaseURL = computed(() => {
  const types = form.accountTypes
  if (types.length === 0) return false
  return types.every((type) => ['apikey', 'upstream'].includes(type))
})

// 切换平台/账号类型后，隐藏的上游地址不能继续留在表单里，否则会被当成
// 有效配置提交（后端也会拒绝这种组合）。
watch(supportsCustomBaseURL, (supported) => {
  if (!supported) {
    form.expectedBaseUrl = ''
  }
})

// 切换平台后：
// - 之前选中的分组 ID 可能属于旧平台，保留会提交出「账号入组与平台不一致」的任务；
// - 之前选中的账号类型可能在新平台不成立（例如从 openai 切到 qoder_cn 只允许 reverse）。
// 两者一起清空，并把类型重置为新平台的可选项。
//
// 这个 watch 必须声明在 `form` 之后：Vue 注册时会立即执行一次 getter，
// 提前访问未初始化的 const 会抛 ReferenceError 并被吞掉，watch 将永远不会生效。
//
// suppressPlatformReset 用于 openEdit/resetForm：它们自己会写入平台与类型，
// 而 watch 在下一个 tick 才执行，不隔离的话会把刚恢复的分组和类型清掉。
const suppressPlatformReset = ref(false)
watch(
  () => form.platform,
  () => {
    if (suppressPlatformReset.value) return
    form.targetGroupIds = []
    const allowed = accountTypesForPlatform(form.platform)
    const kept = form.accountTypes.filter((type) => allowed.includes(type as never))
    form.accountTypes = kept.length > 0 ? kept : allowed.slice(0, 1)
  }
)

function resetForm(): void {
  suppressPlatformReset.value = true
  form.name = ''
  form.description = ''
  form.platform = 'openai'
  form.accountTypes = ['apikey']
  form.rewardGroupId = 0
  form.rewardDays = 30
  form.targetGroupIds = []
  form.expectedBaseUrl = ''
  form.maxPerUser = 1
  form.priority = 50
  form.concurrency = 3
  form.sortOrder = 0
  form.rateMultiplier = ''
  form.verifyModels = true
  form.rejectOnVerifyFail = true
  form.enabled = true
  formError.value = ''
  nextTick(() => { suppressPlatformReset.value = false })
}

function openCreate(): void {
  editingId.value = null
  resetForm()
  editorOpen.value = true
}

function openEdit(task: AdminAccountTask): void {
  suppressPlatformReset.value = true
  editingId.value = task.id
  form.name = task.name
  form.description = task.description
  form.platform = task.platform
  // 历史任务可能存着当前平台不再支持的账号类型，这里收敛到有效集合，
  // 避免打开编辑框就是一个保存必然失败的状态。
  const allowed = accountTypesForPlatform(task.platform)
  const kept = task.account_types.filter((type) =>
    allowed.includes(type as never)
  )
  form.accountTypes = kept.length > 0 ? kept : allowed.slice(0, 1)
  form.rewardGroupId = task.reward_group_id
  form.rewardDays = task.reward_days
  form.targetGroupIds = [...task.target_group_ids]
  form.expectedBaseUrl = task.expected_base_url
  form.maxPerUser = task.max_per_user
  form.priority = task.priority
  form.concurrency = task.concurrency
  form.sortOrder = task.sort_order
  form.rateMultiplier = task.rate_multiplier == null ? '' : String(task.rate_multiplier)
  form.verifyModels = task.verify_models
  form.rejectOnVerifyFail = task.reject_on_verify_fail
  form.enabled = task.enabled
  formError.value = ''
  editorOpen.value = true
  nextTick(() => { suppressPlatformReset.value = false })
}

function closeEditor(): void {
  if (saving.value) return
  editorOpen.value = false
}

async function saveTask(): Promise<void> {
  if (form.accountTypes.length === 0) {
    formError.value = t('admin.accountTasks.typesRequired')
    return
  }
  if (!form.rewardGroupId) {
    formError.value = t('admin.accountTasks.rewardGroupRequired')
    return
  }
  const rateMultiplierRaw = form.rateMultiplier.trim()
  const hasRateMultiplier = rateMultiplierRaw !== ''
  if (hasRateMultiplier) {
    const parsed = Number(rateMultiplierRaw)
    if (!Number.isFinite(parsed) || parsed < 0) {
      formError.value = t('admin.accountTasks.rateMultiplierInvalid')
      return
    }
  }
  const payload: AccountTaskPayload = {
    name: form.name,
    description: form.description,
    platform: form.platform,
    account_types: form.accountTypes,
    reward_group_id: form.rewardGroupId,
    reward_days: form.rewardDays,
    target_group_ids: form.targetGroupIds,
    expected_base_url: supportsCustomBaseURL.value ? form.expectedBaseUrl : '',
    max_per_user: form.maxPerUser,
    priority: form.priority,
    concurrency: form.concurrency,
    sort_order: form.sortOrder,
    rate_multiplier: hasRateMultiplier ? Number(rateMultiplierRaw) : undefined,
    clear_rate_multiplier: !hasRateMultiplier,
    verify_models: form.verifyModels,
    reject_on_verify_fail: form.rejectOnVerifyFail,
    enabled: form.enabled
  }
  saving.value = true
  formError.value = ''
  try {
    if (editingId.value) {
      await updateAccountTask(editingId.value, payload)
    } else {
      await createAccountTask(payload)
    }
    editorOpen.value = false
    appStore.showSuccess(t('admin.accountTasks.saveSuccess'))
    await loadTasks()
  } catch (error) {
    formError.value = extractApiErrorMessage(error, t('admin.accountTasks.saveFailed'))
  } finally {
    saving.value = false
  }
}

async function confirmDelete(task: AdminAccountTask): Promise<void> {
  if (!window.confirm(t('admin.accountTasks.deleteConfirm', { name: task.name }))) return
  try {
    await deleteAccountTask(task.id)
    appStore.showSuccess(t('admin.accountTasks.deleteSuccess'))
    await loadTasks()
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('admin.accountTasks.deleteFailed')))
  }
}

// ---------- 审核 ----------

const reviewOpen = ref(false)
const reviewAction = ref<'approve' | 'reject'>('approve')
const reviewNote = ref('')
const reviewGrantReward = ref(true)
const reviewingId = ref<number | null>(null)
const reviewTarget = ref<AdminAccountSubmission | null>(null)

const reviewTitle = computed(() =>
  reviewAction.value === 'approve'
    ? t('admin.accountTasks.approveTitle')
    : t('admin.accountTasks.rejectTitle')
)

function approve(sub: AdminAccountSubmission): void {
  reviewTarget.value = sub
  reviewAction.value = 'approve'
  reviewNote.value = ''
  reviewGrantReward.value = sub.reward_granted_at === undefined
  reviewOpen.value = true
}

function reject(sub: AdminAccountSubmission): void {
  reviewTarget.value = sub
  reviewAction.value = 'reject'
  reviewNote.value = ''
  reviewOpen.value = true
}

function closeReview(): void {
  if (reviewingId.value !== null) return
  reviewOpen.value = false
  reviewTarget.value = null
}

async function submitReview(): Promise<void> {
  const target = reviewTarget.value
  if (!target) return
  reviewingId.value = target.id
  try {
    if (reviewAction.value === 'approve') {
      await approveSubmission(target.id, {
        note: reviewNote.value,
        grant_reward: reviewGrantReward.value
      })
      appStore.showSuccess(t('admin.accountTasks.approveSuccess'))
    } else {
      await rejectSubmission(target.id, { note: reviewNote.value })
      appStore.showSuccess(t('admin.accountTasks.rejectSuccess'))
    }
    reviewOpen.value = false
    reviewTarget.value = null
    await loadSubmissions()
    await loadTasks()
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('admin.accountTasks.reviewFailed')))
  } finally {
    reviewingId.value = null
  }
}

function testAccount(accountId: number): void {
  window.open(`/admin/accounts?search=${accountId}`, '_blank')
}

// ---------- 数据加载 ----------

async function loadTasks(): Promise<void> {
  loading.value = true
  try {
    tasks.value = await listAccountTasks()
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('admin.accountTasks.loadFailed')))
  } finally {
    loading.value = false
  }
}

async function loadSubmissions(): Promise<void> {
  loadingSubmissions.value = true
  try {
    submissions.value = await listPendingSubmissions()
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('admin.accountTasks.loadSubmissionsFailed')))
  } finally {
    loadingSubmissions.value = false
  }
}

async function loadGroups(): Promise<void> {
  try {
    groups.value = await adminGroupAPI.getAllIncludingInactive()
  } catch {
    groups.value = []
  }
}

onMounted(async () => {
  await Promise.all([loadTasks(), loadSubmissions(), loadGroups()])
})
</script>
