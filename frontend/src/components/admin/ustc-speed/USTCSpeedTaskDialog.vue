<template>
  <BaseDialog
    :show="show"
    :title="task ? t('admin.ustcSpeed.task.edit') : t('admin.ustcSpeed.task.add')"
    width="full"
    :close-on-click-outside="false"
    @close="emit('close')"
  >
    <form ref="formElement" class="space-y-5" @submit.prevent="submit">
      <div class="space-y-4">
        <p v-if="task" class="text-xs text-gray-500">{{ t('admin.ustcSpeed.task.hint.editSnapshot') }}</p>
        <p class="text-xs text-gray-500">{{ t('admin.ustcSpeed.task.hint.sameHours') }}</p>
        <section class="space-y-4 rounded-xl border border-gray-200 p-4 dark:border-dark-700">
          <h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.ustcSpeed.task.target') }}</h3>
          <label class="block space-y-1.5">
            <span class="text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('admin.ustcSpeed.task.field.name') }}</span>
            <input v-model.trim="form.name" required maxlength="120" class="form-input" />
          </label>
          <label class="flex items-start gap-2 text-sm text-gray-700 dark:text-gray-300">
            <input v-model="form.enabled" type="checkbox" class="mt-0.5 rounded border-gray-300 text-primary-600 focus:ring-primary-500" />
            <span>{{ t('admin.ustcSpeed.task.enabled') }}</span>
          </label>
          <p v-if="!form.enabled" class="rounded-lg bg-amber-50 px-3 py-2 text-xs text-amber-800 dark:bg-amber-900/20 dark:text-amber-200">
            {{ t('admin.ustcSpeed.task.disabledByDefault') }}
          </p>
          <label class="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
            <input v-model="form.all_accounts" type="checkbox" class="rounded border-gray-300 text-primary-600 focus:ring-primary-500" />
            {{ t('admin.ustcSpeed.task.allAccounts') }}
          </label>
          <div v-if="!form.all_accounts" class="space-y-2">
            <div class="text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('admin.ustcSpeed.task.selectAccounts') }}</div>
            <div v-if="accounts.length" class="max-h-44 space-y-1 overflow-y-auto rounded-lg border border-gray-200 p-2 dark:border-dark-700">
              <label v-for="account in accounts" :key="account.id" class="flex items-start gap-2 rounded-md px-2 py-1.5 text-sm hover:bg-gray-50 dark:hover:bg-dark-700">
                <input
                  :checked="form.account_ids.includes(account.id)"
                  type="checkbox"
                  class="mt-0.5 rounded border-gray-300 text-primary-600 focus:ring-primary-500"
                  @change="toggleAccount(account.id)"
                />
                <span class="min-w-0 flex-1">
                  <span class="block truncate text-gray-800 dark:text-gray-200">{{ account.name }} <span class="text-gray-400">#{{ account.id }}</span></span>
                  <span v-if="account.models.length" class="block truncate text-xs text-gray-400">{{ account.models.join(', ') }}</span>
                  <span v-if="account.key_account_id && account.key_account_id !== account.id" class="block text-xs text-gray-400">{{ t('admin.ustcSpeed.task.hint.physicalKeyGroup', { id: account.key_account_id }) }}</span>
                </span>
              </label>
            </div>
            <p v-else class="text-xs text-gray-500">{{ t('admin.ustcSpeed.task.noAccounts') }}</p>
          </div>
          <div class="space-y-2">
            <div class="text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('admin.ustcSpeed.task.selectModels') }}</div>
            <div v-if="models.length" class="max-h-36 space-y-1 overflow-y-auto rounded-lg border border-gray-200 p-2 dark:border-dark-700">
              <label v-for="model in models" :key="model" class="flex items-center gap-2 rounded-md px-2 py-1.5 text-sm hover:bg-gray-50 dark:hover:bg-dark-700">
                <input
                  :checked="form.models.includes(model)"
                  type="checkbox"
                  class="rounded border-gray-300 text-primary-600 focus:ring-primary-500"
                  @change="toggleModel(model)"
                />
                <span class="truncate text-gray-800 dark:text-gray-200">{{ model }}</span>
              </label>
            </div>
            <p v-else class="text-xs text-gray-500">{{ t('admin.ustcSpeed.task.noModels') }}</p>
            <div class="flex gap-2">
              <input v-model.trim="modelEntry" class="form-input" :placeholder="t('admin.ustcSpeed.task.modelPlaceholder')" @keydown.enter.prevent="addManualModel" />
              <button type="button" class="btn btn-secondary shrink-0" @click="addManualModel">{{ t('admin.ustcSpeed.task.addModel') }}</button>
            </div>
            <div v-if="form.models.length" class="flex flex-wrap gap-1.5">
              <span v-for="model in form.models" :key="model" class="inline-flex items-center gap-1 rounded-full bg-primary-50 px-2.5 py-1 text-xs text-primary-700 dark:bg-primary-900/25 dark:text-primary-300">
                {{ model }}
                <button type="button" class="font-semibold" :aria-label="t('admin.ustcSpeed.task.removeModel', { model })" @click="toggleModel(model)">×</button>
              </span>
            </div>
          </div>
          <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ustcSpeed.task.hint.capacities') }}</p>
        </section>

        <section class="space-y-4 rounded-xl border border-gray-200 p-4 dark:border-dark-700">
          <h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.ustcSpeed.task.schedule') }}</h3>
          <label class="flex items-start gap-3 rounded-lg bg-gray-50 p-3 text-sm dark:bg-dark-900/40">
            <input v-model="form.ignore_task_constraints" data-testid="ignore-task-constraints" type="checkbox" class="mt-0.5 rounded border-gray-300 text-primary-600 focus:ring-primary-500" />
            <span><span class="block font-medium text-gray-800 dark:text-gray-200">{{ t('admin.ustcSpeed.task.ignoreConstraints') }}</span><span class="mt-1 block text-xs leading-5 text-gray-500 dark:text-gray-400">{{ t('admin.ustcSpeed.task.ignoreConstraintsHint') }}</span></span>
          </label>
          <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
            <label class="block space-y-1.5">
              <span class="block text-xs font-medium text-gray-600 dark:text-gray-400">{{ t('admin.ustcSpeed.task.field.interval') }}</span>
              <span class="flex gap-2">
                <input
                  :value="intervalInputValue"
                  type="number"
                  :min="intervalInputMin"
                  :max="86400 / intervalUnitSeconds[intervalUnit]"
                  step="any"
                  required
                  class="form-input min-w-0 flex-1"
                  @input="updateIntervalFromInput"
                />
                <select v-model="intervalUnit" class="form-input w-32 shrink-0">
                  <option value="seconds">{{ t('admin.ustcSpeed.task.intervalUnit.seconds') }}</option>
                  <option value="minutes">{{ t('admin.ustcSpeed.task.intervalUnit.minutes') }}</option>
                  <option value="hours">{{ t('admin.ustcSpeed.task.intervalUnit.hours') }}</option>
                </select>
              </span>
            </label>
            <NumberField v-model="form.jitter_seconds" :label="t('admin.ustcSpeed.task.field.jitter')" :min="0" :max="Math.max(0, form.interval_seconds - 1)" v-if="!form.cron_expression" />
            <TextField v-model="form.timezone" :label="t('admin.ustcSpeed.task.field.timezone')" />
            <TextField v-model="form.active_start" :label="t('admin.ustcSpeed.task.field.activeStart')" type="time" />
            <TextField v-model="form.active_end" :label="t('admin.ustcSpeed.task.field.activeEnd')" type="time" />
          </div>
          <p v-if="form.cron_expression" class="rounded-lg bg-blue-50 px-3 py-2 text-xs text-blue-800 dark:bg-blue-900/20 dark:text-blue-200">
            {{ t('admin.ustcSpeed.task.hint.cronOverridesInterval') }}
          </p>
          <div>
            <div class="mb-2 text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('admin.ustcSpeed.task.field.days') }}</div>
            <div class="flex flex-wrap gap-2">
              <label v-for="day in weekdays" :key="day.value" class="inline-flex cursor-pointer items-center gap-1.5 rounded-lg border border-gray-200 px-2.5 py-1.5 text-xs text-gray-700 dark:border-dark-600 dark:text-gray-300">
                <input v-model="form.days_of_week" type="checkbox" :value="day.value" class="rounded border-gray-300 text-primary-600 focus:ring-primary-500" />
                {{ day.label }}
              </label>
            </div>
            <p class="mt-1.5 text-xs text-gray-500">{{ t('admin.ustcSpeed.task.hint.days') }}</p>
          </div>
        </section>
      </div>

      <section class="space-y-4 rounded-xl border border-gray-200 p-4 dark:border-dark-700">
        <h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.ustcSpeed.task.request') }}</h3>
        <div class="grid gap-4 md:grid-cols-3">
          <label class="block space-y-1.5">
            <span class="text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('admin.ustcSpeed.task.field.apiMode') }}</span>
            <select v-model="form.api_mode" class="form-input">
              <option value="chat_completions">{{ t('admin.ustcSpeed.task.apis.chat') }}</option>
              <option value="responses">{{ t('admin.ustcSpeed.task.apis.responses') }}</option>
            </select>
          </label>
          <label class="block space-y-1.5">
            <span class="text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('admin.ustcSpeed.task.field.thinkingMode') }}</span>
            <select v-model="form.thinking_mode" class="form-input">
              <option value="off">{{ t('admin.ustcSpeed.task.thinking.off') }}</option>
              <option value="on">{{ t('admin.ustcSpeed.task.thinking.on') }}</option>
              <option value="auto">{{ t('admin.ustcSpeed.task.thinking.auto') }}</option>
            </select>
          </label>
          <label class="block space-y-1.5">
            <span class="text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('admin.ustcSpeed.task.field.reasoningEffort') }}</span>
            <select v-model="form.reasoning_effort" class="form-input">
              <option value="">{{ t('admin.ustcSpeed.task.effort.none') }}</option>
              <option value="low">{{ t('admin.ustcSpeed.task.effort.low') }}</option>
              <option value="medium">{{ t('admin.ustcSpeed.task.effort.medium') }}</option>
              <option value="high">{{ t('admin.ustcSpeed.task.effort.high') }}</option>
              <option value="xhigh">{{ t('admin.ustcSpeed.task.effort.xhigh') }}</option>
            </select>
          </label>
          <NumberField v-model="form.max_tokens" :label="t('admin.ustcSpeed.task.field.maxTokens')" :min="16" :max="8192" />
          <NumberField v-model="form.temperature" :label="t('admin.ustcSpeed.task.field.temperature')" :min="0" :max="2" :step="0.1" />
          <NumberField v-model="topPValue" :label="t('admin.ustcSpeed.task.field.topP')" :min="0" :max="1" :step="0.05" optional />
          <NumberField v-model="seedValue" :label="t('admin.ustcSpeed.task.field.seed')" v-if="form.api_mode !== 'responses'" optional />
          <NumberField v-model="form.timeout_seconds" :label="t('admin.ustcSpeed.task.field.timeout')" :min="5" :max="180" />
          <NumberField v-model="form.first_output_timeout_seconds" :label="t('admin.ustcSpeed.task.field.firstOutputTimeout')" :min="1" :max="form.timeout_seconds" />
        </div>
        <label class="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
          <input v-model="form.stream" type="checkbox" class="rounded border-gray-300 text-primary-600 focus:ring-primary-500" />
          {{ t('admin.ustcSpeed.task.field.stream') }}
        </label>
        <label class="block space-y-1.5">
          <span class="text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('admin.ustcSpeed.task.field.prompt') }}</span>
          <textarea v-model="form.prompt" rows="3" class="form-input resize-y" />
          <span class="block text-xs text-gray-500">{{ t('admin.ustcSpeed.task.field.promptHint') }}</span>
        </label>
      </section>

      <details class="rounded-xl border border-gray-200 p-4 dark:border-dark-700">
        <summary class="cursor-pointer text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.ustcSpeed.task.advanced') }}</summary>
        <div class="mt-4 space-y-5">
          <section class="space-y-3 rounded-lg border border-gray-100 p-3 dark:border-dark-700">
            <h4 class="text-sm font-medium text-gray-800 dark:text-gray-200">{{ t('admin.ustcSpeed.task.advancedSchedule') }}</h4>
            <TextField v-model="form.cron_expression" :label="t('admin.ustcSpeed.task.field.cronExpression')" />
            <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ustcSpeed.task.hint.cronExpression') }}</p>
          </section>
          <div class="grid gap-4 md:grid-cols-3">
            <NumberField v-model="form.max_parallel_accounts" :label="t('admin.ustcSpeed.task.field.maxParallel')" :min="1" :max="16" />
            <NumberField v-model="form.max_start_skew_seconds" :label="t('admin.ustcSpeed.task.field.maxStartSkew')" :min="1" :max="30" />
            <NumberField v-model="form.reserve_rpm" :label="t('admin.ustcSpeed.task.field.reserveRpm')" :min="0" />
            <NumberField v-model="form.reserve_concurrency" :label="t('admin.ustcSpeed.task.field.reserveConcurrency')" :min="1" />
            <NumberField v-model="form.max_active_requests" :label="t('admin.ustcSpeed.task.field.maxActive')" :min="0" />
          </div>
          <label class="flex items-start gap-2 text-sm text-gray-700 dark:text-gray-300">
            <input v-model="form.skip_if_user_waiting" type="checkbox" class="mt-0.5 rounded border-gray-300 text-primary-600 focus:ring-primary-500" />
            <span>{{ t('admin.ustcSpeed.task.field.skipWaiting') }}</span>
          </label>
          <p class="-mt-3 text-xs text-gray-500">{{ t('admin.ustcSpeed.task.hint.perKeyConcurrency') }} {{ t('admin.ustcSpeed.task.hint.zeroUnlimited') }}</p>
          <div class="grid gap-4 md:grid-cols-3">
            <NumberField v-model="form.backoff_after_429" :label="t('admin.ustcSpeed.task.field.backoffAfter429')" :min="1" :max="20" />
            <NumberField v-model="form.backoff_window_seconds" :label="t('admin.ustcSpeed.task.field.backoffWindow')" :min="60" :max="86400" />
            <NumberField v-model="form.backoff_multiplier" :label="t('admin.ustcSpeed.task.field.backoffMultiplier')" :min="1" :max="10" :step="0.5" />
            <NumberField v-model="form.max_interval_seconds" :label="t('admin.ustcSpeed.task.field.maxInterval')" :min="form.interval_seconds" :max="86400" />
            <NumberField v-model="form.pause_after_429" :label="t('admin.ustcSpeed.task.field.pauseAfter429')" :min="form.backoff_after_429" :max="40" />
            <NumberField v-model="form.recovery_successes" :label="t('admin.ustcSpeed.task.field.recoverySuccesses')" :min="1" :max="20" />
          </div>
          <div class="grid gap-4 md:grid-cols-2">
            <TextAreaField v-model="promptVariantsText" :label="t('admin.ustcSpeed.task.field.promptVariants')" />
            <TextAreaField v-if="form.api_mode !== 'responses'" v-model="stopText" :label="t('admin.ustcSpeed.task.field.stop')" />
          </div>
        </div>
      </details>

      <p v-if="validationError" role="alert" class="rounded-lg bg-red-50 px-3 py-2 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-200">
        {{ validationError }}
      </p>
    </form>

    <template #footer>
      <div class="flex flex-wrap items-center justify-between gap-3">
        <span class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ustcSpeed.task.disabledByDefault') }}</span>
        <div class="flex gap-2">
          <button type="button" class="btn btn-secondary" :disabled="saving" @click="emit('close')">{{ t('admin.ustcSpeed.action.cancel') }}</button>
          <button type="button" class="btn btn-primary" :disabled="saving" @click="submit">
            {{ saving ? t('admin.ustcSpeed.action.saving') : t('admin.ustcSpeed.action.save') }}
          </button>
        </div>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, defineComponent, h, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import type { USTCSpeedAccountDimension, USTCSpeedTask, USTCSpeedTaskInput } from '@/api/admin/ustcSpeed'

const props = defineProps<{
  show: boolean
  task: USTCSpeedTask | null
  accounts: USTCSpeedAccountDimension[]
  models: string[]
  saving?: boolean
  defaults?: USTCSpeedTaskInput
}>()

const emit = defineEmits<{
  (event: 'close'): void
  (event: 'submit', value: USTCSpeedTaskInput): void
}>()

const { t } = useI18n()
const validationError = ref('')
const modelEntry = ref('')
const formElement = ref<HTMLFormElement | null>(null)

const defaults = (): USTCSpeedTaskInput => ({
  id: props.task?.id ?? 0,
  name: '',
  enabled: false,
  all_accounts: false,
  ignore_task_constraints: false,
  account_ids: [],
  models: [],
  interval_seconds: 1800,
  jitter_seconds: 180,
  cron_expression: '',
  timezone: 'Asia/Shanghai',
  active_start: '00:00',
  active_end: '23:59',
  api_mode: 'chat_completions',
  stream: true,
  prompt: 'Generate a numbered list of 120 practical suggestions for keeping a personal research notebook. Each item must be one short English sentence. Start immediately with item 1; do not add an introduction or conclusion.',
  prompt_variants: [],
  max_tokens: 512,
  temperature: 0,
  top_p: null,
  seed: null,
  stop: [],
  thinking_mode: 'off',
  reasoning_effort: '',
  timeout_seconds: 60,
  first_output_timeout_seconds: 30,
  max_parallel_accounts: 4,
  max_start_skew_seconds: 5,
  reserve_rpm: 4,
  reserve_concurrency: 2,
  max_active_requests: 16,
  skip_if_user_waiting: true,
  backoff_after_429: 3,
  backoff_window_seconds: 900,
  backoff_multiplier: 2,
  max_interval_seconds: 7200,
  pause_after_429: 6,
  recovery_successes: 2,
  ...props.defaults,
  days_of_week: props.defaults?.days_of_week?.length ? [...props.defaults.days_of_week] : [0,1,2,3,4,5,6],
})

const form = ref<USTCSpeedTaskInput>(defaults())

type IntervalUnit = 'seconds' | 'minutes' | 'hours'
const intervalUnit = ref<IntervalUnit>('minutes')
const intervalUnitSeconds: Record<IntervalUnit, number> = { seconds: 1, minutes: 60, hours: 3600 }
const intervalInputValue = computed(() => String(form.value.interval_seconds / intervalUnitSeconds[intervalUnit.value]))
const intervalInputMin = computed(() => 60 / intervalUnitSeconds[intervalUnit.value])

function preferredIntervalUnit(seconds: number): IntervalUnit {
  if (seconds >= 3600 && seconds % 3600 === 0) return 'hours'
  if (seconds >= 60 && seconds % 60 === 0) return 'minutes'
  return 'seconds'
}

function updateIntervalFromInput(event: Event) {
  const raw = (event.target as HTMLInputElement).value
  if (!raw.trim()) {
    form.value.interval_seconds = Number.NaN
    return
  }
  const value = Number(raw)
  const intervalSeconds = Number.isFinite(value)
    ? Math.round(value * intervalUnitSeconds[intervalUnit.value])
    : Number.NaN
  form.value.interval_seconds = intervalSeconds
  if (Number.isFinite(intervalSeconds)) {
    form.value.max_interval_seconds = Math.max(form.value.max_interval_seconds, intervalSeconds)
    form.value.jitter_seconds = Math.min(form.value.jitter_seconds, Math.max(intervalSeconds - 1, 0))
  }
}

const weekdays = computed(() => [
  { value: 0, label: t('admin.ustcSpeed.task.weekday.sun') },
  { value: 1, label: t('admin.ustcSpeed.task.weekday.mon') },
  { value: 2, label: t('admin.ustcSpeed.task.weekday.tue') },
  { value: 3, label: t('admin.ustcSpeed.task.weekday.wed') },
  { value: 4, label: t('admin.ustcSpeed.task.weekday.thu') },
  { value: 5, label: t('admin.ustcSpeed.task.weekday.fri') },
  { value: 6, label: t('admin.ustcSpeed.task.weekday.sat') },
])

const topPValue = computed<string | number>({
  get: () => form.value.top_p ?? '',
  set: (value) => { form.value.top_p = value === '' ? null : Number(value) },
})
const seedValue = computed<string | number>({
  get: () => form.value.seed ?? '',
  set: (value) => { form.value.seed = value === '' ? null : Number(value) },
})
const stopText = computed({
  get: () => form.value.stop.join('\n'),
  set: (value: string) => { form.value.stop = splitLines(value) },
})
const promptVariantsText = computed({
  get: () => form.value.prompt_variants.join('\n'),
  set: (value: string) => { form.value.prompt_variants = splitLines(value) },
})

watch(() => [props.show, props.task] as const, ([show, task]) => {
  if (!show) return
  validationError.value = ''
  modelEntry.value = ''
  if (!task) {
    form.value = defaults()
    intervalUnit.value = preferredIntervalUnit(form.value.interval_seconds)
    return
  }
  form.value = {
    ...defaults(),
    ...task,
    account_ids: task.account_ids ?? [],
    models: task.models ?? [],
    days_of_week: task.days_of_week?.length ? [...task.days_of_week] : [0,1,2,3,4,5,6],
    prompt_variants: task.prompt_variants ?? [],
    stop: task.stop ?? [],
    top_p: task.top_p ?? null,
    seed: task.seed ?? null,
  }
  intervalUnit.value = preferredIntervalUnit(task.interval_seconds)
}, { immediate: true })

function splitLines(value: string): string[] {
  return value.split(/\r?\n/).map((line) => line.trim()).filter(Boolean)
}

function toggleAccount(id: number) {
  form.value.account_ids = form.value.account_ids.includes(id)
    ? form.value.account_ids.filter((value) => value !== id)
    : [...form.value.account_ids, id]
}

function toggleModel(model: string) {
  form.value.models = form.value.models.includes(model)
    ? form.value.models.filter((value) => value !== model)
    : [...form.value.models, model]
}

function addManualModel() {
  const model = modelEntry.value.trim()
  if (model && !form.value.models.includes(model)) form.value.models = [...form.value.models, model]
  modelEntry.value = ''
}

function submit() {
  validationError.value = ''
  const requiredNumbers = [
    form.value.interval_seconds,
    form.value.jitter_seconds,
    form.value.max_tokens,
    form.value.temperature,
    form.value.timeout_seconds,
    form.value.first_output_timeout_seconds,
    form.value.max_parallel_accounts,
    form.value.max_start_skew_seconds,
    form.value.reserve_rpm,
    form.value.reserve_concurrency,
    form.value.max_active_requests,
    form.value.backoff_after_429,
    form.value.backoff_window_seconds,
    form.value.backoff_multiplier,
    form.value.max_interval_seconds,
    form.value.pause_after_429,
    form.value.recovery_successes,
    ...(form.value.top_p === null ? [] : [form.value.top_p]),
    ...(form.value.seed === null ? [] : [form.value.seed]),
  ]
  if (requiredNumbers.some((value) => !Number.isFinite(value))) {
    validationError.value = t('admin.ustcSpeed.task.validation.numbers')
    return
  }
  if (!Number.isInteger(form.value.interval_seconds) || form.value.interval_seconds < 60) {
    validationError.value = t('admin.ustcSpeed.task.validation.numbers')
    return
  }
  const bounds: Array<[keyof USTCSpeedTaskInput, string, number, number, boolean]> = [
    ['interval_seconds','interval',60,86400,true], ['jitter_seconds','jitter',0,form.value.cron_expression ? Infinity : form.value.interval_seconds-1,true],
    ['max_tokens','maxTokens',16,8192,true], ['temperature','temperature',0,2,false],
    ['timeout_seconds','timeout',5,180,true], ['first_output_timeout_seconds','firstOutputTimeout',1,form.value.timeout_seconds,true],
    ['max_parallel_accounts','maxParallel',1,16,true], ['max_start_skew_seconds','maxStartSkew',1,30,true],
    ['reserve_rpm','reserveRpm',0,Infinity,true], ['reserve_concurrency','reserveConcurrency',1,Infinity,true], ['max_active_requests','maxActive',0,Infinity,true],
    ['backoff_after_429','backoffAfter429',1,20,true], ['backoff_window_seconds','backoffWindow',60,86400,true],
    ['backoff_multiplier','backoffMultiplier',1,10,false], ['max_interval_seconds','maxInterval',form.value.interval_seconds,86400,true],
    ['pause_after_429','pauseAfter429',form.value.backoff_after_429,40,true], ['recovery_successes','recoverySuccesses',1,20,true],
  ]
  for (const [key,label,min,max,integer] of bounds) {
    const value = Number(form.value[key])
    if (value < min || value > max || (integer && !Number.isInteger(value))) {
      validationError.value = t('admin.ustcSpeed.task.validation.range', { field: t(`admin.ustcSpeed.task.field.${label}`), min, max: Number.isFinite(max) ? max : '∞' })
      return
    }
  }
  if (form.value.top_p != null && (form.value.top_p <= 0 || form.value.top_p > 1)) { validationError.value = '0 < Top P ≤ 1'; return }
  if (form.value.seed != null && !Number.isSafeInteger(form.value.seed)) { validationError.value = t('admin.ustcSpeed.task.validation.numbers'); return }
  if (!form.value.days_of_week.length) { validationError.value = t('admin.ustcSpeed.task.validation.days'); return }
  if (!form.value.prompt.trim() || new TextEncoder().encode(form.value.prompt).length > 128000 || form.value.prompt_variants.length > 16 || form.value.stop.length > 8) { validationError.value = t('admin.ustcSpeed.task.validation.request'); return }
  try { new Intl.DateTimeFormat('en', { timeZone: form.value.timezone }) } catch { validationError.value = t('admin.ustcSpeed.task.validation.timezone'); return }
  if (formElement.value && !formElement.value.reportValidity()) return
  if (!form.value.name.trim()) {
    validationError.value = t('admin.ustcSpeed.task.validation.name')
    return
  }
  if (!form.value.models.length || form.value.models.length > 20) {
    validationError.value = t('admin.ustcSpeed.task.validation.models')
    return
  }
  if (!form.value.all_accounts && !form.value.account_ids.length) {
    validationError.value = t('admin.ustcSpeed.task.validation.accounts')
    return
  }
  emit('submit', {
    ...form.value,
    account_ids: form.value.all_accounts ? [] : [...form.value.account_ids],
    models: [...form.value.models],
    prompt_variants: [...form.value.prompt_variants],
    stop: [...form.value.stop],
    days_of_week: [...form.value.days_of_week].sort((a, b) => a - b),
  })
}

const fieldClass = 'w-full rounded-lg border border-gray-300 bg-white px-3 py-2 text-sm text-gray-900 outline-none transition focus:border-primary-500 focus:ring-2 focus:ring-primary-500/20 dark:border-dark-600 dark:bg-dark-800 dark:text-white'

const NumberField = defineComponent({
  props: { modelValue: { type: [Number, String], required: true }, label: { type: String, required: true }, min: Number, max: Number, step: Number, optional: Boolean },
  emits: ['update:modelValue'],
  setup(fieldProps, { emit: fieldEmit }) {
    return () => h('label', { class: 'block space-y-1.5' }, [
      h('span', { class: 'block text-xs font-medium text-gray-600 dark:text-gray-400' }, fieldProps.label),
      h('input', {
        class: fieldClass,
        type: 'number',
        min: fieldProps.min,
        max: fieldProps.max,
        step: fieldProps.step ?? 1,
        required: !fieldProps.optional,
        value: fieldProps.modelValue,
        placeholder: fieldProps.optional ? '—' : undefined,
        onInput: (event: Event) => {
          const raw = (event.target as HTMLInputElement).value
          fieldEmit('update:modelValue', raw === '' ? (fieldProps.optional ? '' : Number.NaN) : Number(raw))
        },
      }),
    ])
  },
})

const TextField = defineComponent({
  props: { modelValue: { type: String, required: true }, label: { type: String, required: true }, type: { type: String, default: 'text' } },
  emits: ['update:modelValue'],
  setup(fieldProps, { emit: fieldEmit }) {
    return () => h('label', { class: 'block space-y-1.5' }, [
      h('span', { class: 'block text-xs font-medium text-gray-600 dark:text-gray-400' }, fieldProps.label),
      h('input', { class: fieldClass, type: fieldProps.type, value: fieldProps.modelValue, onInput: (event: Event) => fieldEmit('update:modelValue', (event.target as HTMLInputElement).value) }),
    ])
  },
})

const TextAreaField = defineComponent({
  props: { modelValue: { type: String, required: true }, label: { type: String, required: true } },
  emits: ['update:modelValue'],
  setup(fieldProps, { emit: fieldEmit }) {
    return () => h('label', { class: 'block space-y-1.5' }, [
      h('span', { class: 'block text-xs font-medium text-gray-600 dark:text-gray-400' }, fieldProps.label),
      h('textarea', { class: `${fieldClass} resize-y`, rows: 3, value: fieldProps.modelValue, onInput: (event: Event) => fieldEmit('update:modelValue', (event.target as HTMLTextAreaElement).value) }),
    ])
  },
})
</script>
