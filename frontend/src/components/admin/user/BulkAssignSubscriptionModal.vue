<template>
  <BaseDialog
    :show="show"
    :title="t('admin.users.bulkAssign.title')"
    width="normal"
    :show-close-button="!submitting"
    :close-on-escape="!submitting"
    @close="handleClose"
  >
    <form
      id="bulk-assign-subscription-form"
      class="space-y-5"
      @submit.prevent="handleSubmit"
    >
      <p class="text-sm font-medium text-gray-700 dark:text-gray-300">
        {{ t('admin.users.bulkAssign.selectedCount', { count: selectedIds.length }) }}
      </p>

      <div>
        <label class="input-label">{{ t('admin.users.bulkAssign.group') }}</label>
        <Select
          v-model="groupId"
          :options="groupOptions"
          :disabled="submitting || subscriptionGroups.length === 0"
          :placeholder="t('admin.users.bulkAssign.selectGroup')"
        >
          <template #selected="{ option }">
            <GroupBadge
              v-if="option"
              :name="(option as unknown as GroupOption).label"
              :platform="(option as unknown as GroupOption).platform"
              :subscription-type="(option as unknown as GroupOption).subscriptionType"
              :rate-multiplier="(option as unknown as GroupOption).rate"
            />
            <span v-else class="text-gray-400">{{ t('admin.users.bulkAssign.selectGroup') }}</span>
          </template>
          <template #option="{ option, selected }">
            <GroupOptionItem
              :name="(option as unknown as GroupOption).label"
              :platform="(option as unknown as GroupOption).platform"
              :subscription-type="(option as unknown as GroupOption).subscriptionType"
              :rate-multiplier="(option as unknown as GroupOption).rate"
              :description="(option as unknown as GroupOption).description"
              :selected="selected"
            />
          </template>
        </Select>
        <p v-if="subscriptionGroups.length === 0" class="input-hint">
          {{ t('admin.users.bulkAssign.noGroups') }}
        </p>
      </div>

      <div>
        <label class="input-label">{{ t('admin.users.bulkAssign.validityDays') }}</label>
        <input
          v-model.number="validityDays"
          type="number"
          min="1"
          max="36500"
          step="1"
          :disabled="submitting"
          class="input"
          data-test="validity-days"
        />
        <p class="input-hint">{{ t('admin.users.bulkAssign.validityHint') }}</p>
      </div>

      <p class="input-hint">{{ t('admin.users.bulkAssign.semanticsHint') }}</p>
      <p v-if="batchCount > 1" class="input-hint" data-test="batch-hint">
        {{ t('admin.users.bulkAssign.batchHint', { count: selectedIds.length, batches: batchCount }) }}
      </p>

      <div v-if="submitting" class="space-y-1" role="status">
        <p class="text-sm text-gray-600 dark:text-gray-400">
          {{ t('admin.users.bulkAssign.progress', { processed: progress.processed, total: progress.total }) }}
        </p>
        <div class="h-2 w-full overflow-hidden rounded-full bg-gray-200 dark:bg-dark-700">
          <div class="h-full bg-primary-600 transition-all" :style="{ width: `${progressPercent}%` }"></div>
        </div>
      </div>

      <div v-if="result" class="space-y-2 text-sm" role="status" data-test="bulk-assign-result">
        <p>
          {{ t('admin.users.bulkAssign.resultSuccess', {
            success: result.success,
            created: result.created,
            reused: result.reused,
            failed: result.failed
          }) }}
        </p>
        <ul
          v-if="result.errors.length"
          class="max-h-40 space-y-1 overflow-y-auto text-red-600 dark:text-red-400"
          data-test="bulk-assign-errors"
        >
          <li v-for="(error, index) in result.errors" :key="index">{{ error }}</li>
        </ul>
      </div>
    </form>

    <template #footer>
      <div class="flex justify-end gap-3">
        <button type="button" class="btn btn-secondary" :disabled="submitting" @click="handleClose">
          {{ result ? t('common.close') : t('common.cancel') }}
        </button>
        <button
          v-if="result && result.failedIds.length > 0 && !submitting"
          type="button"
          class="btn btn-secondary"
          data-test="retry-failed"
          @click="retryFailed"
        >
          {{ t('admin.users.bulkAssign.retry', { count: result.failedIds.length }) }}
        </button>
        <button
          v-if="!result"
          type="submit"
          form="bulk-assign-subscription-form"
          class="btn btn-primary"
          :disabled="!canSubmit"
          data-test="submit"
        >
          {{ submitting ? t('admin.users.bulkAssign.submitting') : t('admin.users.bulkAssign.submit') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { extractApiErrorMessage } from '@/utils/apiError'
import { useAppStore } from '@/stores/app'
import type { AdminGroup, GroupPlatform, SubscriptionType } from '@/types'
import type { SelectOption } from '@/components/common/Select.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select from '@/components/common/Select.vue'
import GroupBadge from '@/components/common/GroupBadge.vue'
import GroupOptionItem from '@/components/common/GroupOptionItem.vue'

// Backend caps user_ids at 100 per bulk-assign request.
const BATCH_SIZE = 100

interface GroupOption {
  value: number
  label: string
  description: string | null
  platform: GroupPlatform
  subscriptionType: SubscriptionType
  rate: number
}

interface BulkAssignSummary {
  success: number
  created: number
  reused: number
  failed: number
  errors: string[]
  failedIds: number[]
}

const props = defineProps<{
  show: boolean
  selectedIds: number[]
  groups: AdminGroup[]
}>()

const emit = defineEmits<{
  close: []
  success: []
}>()

const { t } = useI18n()
const appStore = useAppStore()

const groupId = ref<number | null>(null)
const validityDays = ref(30)
const submitting = ref(false)
const result = ref<BulkAssignSummary | null>(null)
const progress = reactive({ processed: 0, total: 0 })

const subscriptionGroups = computed(() =>
  props.groups.filter(
    (group) => group.subscription_type === 'subscription' && group.status === 'active'
  )
)

const groupOptions = computed<SelectOption[]>(() =>
  subscriptionGroups.value.map((group) => ({
    value: group.id,
    label: group.name,
    description: group.description || null,
    platform: group.platform,
    subscriptionType: 'subscription',
    rate: group.rate_multiplier
  }))
)

const selectedGroupName = computed(
  () => subscriptionGroups.value.find((group) => group.id === groupId.value)?.name || ''
)

const batchCount = computed(() => Math.max(1, Math.ceil(props.selectedIds.length / BATCH_SIZE)))
const progressPercent = computed(() =>
  progress.total === 0 ? 0 : Math.floor((progress.processed / progress.total) * 100)
)

const validityValid = computed(
  () => Number.isInteger(validityDays.value) && validityDays.value >= 1 && validityDays.value <= 36500
)
const canSubmit = computed(
  () =>
    !submitting.value &&
    props.selectedIds.length > 0 &&
    groupId.value !== null &&
    validityValid.value
)

const reset = () => {
  groupId.value = null
  validityDays.value = 30
  submitting.value = false
  result.value = null
  progress.processed = 0
  progress.total = 0
}

watch(
  () => props.show,
  (show) => {
    if (show) reset()
  }
)

const handleClose = () => {
  if (submitting.value) return
  emit('close')
}

const submitIds = async (ids: number[]) => {
  const targetGroupId = groupId.value
  const days = validityDays.value
  if (ids.length === 0 || targetGroupId === null) return

  submitting.value = true
  progress.processed = 0
  progress.total = ids.length
  const summary: BulkAssignSummary = {
    success: 0,
    created: 0,
    reused: 0,
    failed: 0,
    errors: [],
    failedIds: []
  }

  try {
    for (let offset = 0; offset < ids.length; offset += BATCH_SIZE) {
      const chunk = ids.slice(offset, offset + BATCH_SIZE)
      try {
        const batch = await adminAPI.subscriptions.bulkAssign({
          user_ids: chunk,
          group_id: targetGroupId,
          validity_days: days
        })
        summary.success += batch.success_count
        summary.created += batch.created_count
        summary.reused += batch.reused_count
        summary.failed += batch.failed_count
        if (batch.errors?.length) summary.errors.push(...batch.errors)
        if (batch.statuses) {
          for (const [id, status] of Object.entries(batch.statuses)) {
            if (status === 'failed') summary.failedIds.push(Number(id))
          }
        }
        progress.processed += chunk.length
      } catch (error) {
        // A failed request leaves the rest of this batch and all later batches untouched.
        // Mark them as failed so the retry action resubmits them.
        summary.failed += ids.length - offset
        summary.failedIds.push(...ids.slice(offset))
        appStore.showError(
          extractApiErrorMessage(error, t('admin.users.bulkAssign.failed'))
        )
        break
      }
    }
  } finally {
    submitting.value = false
  }

  result.value = summary
  if (summary.success > 0) emit('success')
}

const handleSubmit = () => {
  if (!canSubmit.value || groupId.value === null) return
  const confirmed = window.confirm(
    t('admin.users.bulkAssign.confirm', {
      count: props.selectedIds.length,
      group: selectedGroupName.value,
      days: validityDays.value
    })
  )
  if (!confirmed) return
  void submitIds([...props.selectedIds])
}

const retryFailed = () => {
  const failedIds = result.value?.failedIds ?? []
  if (failedIds.length === 0) return
  result.value = null
  void submitIds([...failedIds])
}
</script>
