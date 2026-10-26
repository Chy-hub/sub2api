<template>
  <section id="benchmark-samples" class="scroll-mt-24 overflow-hidden rounded-2xl border border-gray-200 bg-white shadow-sm dark:border-dark-700 dark:bg-dark-800" data-testid="benchmark-samples">
    <div class="flex flex-wrap items-center justify-between gap-3 border-b border-gray-100 px-5 py-4 dark:border-dark-700">
      <div>
        <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.ustcSpeed.samples.tableTitle') }}</h2>
        <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ustcSpeed.samples.description') }}</p>
      </div>
      <div class="flex items-center gap-3">
        <span class="text-xs tabular-nums text-gray-500">{{ t('admin.ustcSpeed.samples.total', { count: total }) }}</span>
        <button type="button" class="btn btn-secondary" :disabled="loading" @click="load()">{{ t('admin.ustcSpeed.action.refresh') }}</button>
      </div>
    </div>
    <div v-if="error" role="alert" class="border-b border-amber-100 bg-amber-50 px-5 py-3 text-sm text-amber-700 dark:border-amber-900/30 dark:bg-amber-900/10 dark:text-amber-300">{{ t('admin.ustcSpeed.error.loadSamples') }}</div>
    <DataTable :data="items" :columns="columns" :loading="loading" row-key="id" :clickable-rows="true" :expandable-actions="false" @row-click="(sample) => emit('open', sample.id)">
      <template #cell-started_at="{ row }"><span class="whitespace-nowrap text-xs tabular-nums" :title="row.started_at">{{ timestamp(row.started_at) }}</span></template>
      <template #cell-task_id="{ row }"><span class="block max-w-44 truncate" :title="taskName(row.task_id)">{{ taskName(row.task_id) }}</span><span v-if="row.round_id" class="text-[10px] text-gray-400" :title="row.round_id">{{ row.round_id.slice(0, 8) }}</span></template>
      <template #cell-account_name="{ row }"><span class="block max-w-40 truncate" :title="row.account_name">{{ row.account_name || `#${row.account_id}` }}</span></template>
      <template #cell-model="{ row }"><span class="block max-w-56 truncate text-xs" :title="row.model">{{ row.model }}</span><span class="text-[10px] text-gray-400">{{ t(row.stream ? 'admin.ustcSpeed.filter.streaming' : 'admin.ustcSpeed.filter.nonStreaming') }}</span></template>
      <template #cell-status="{ row }"><span class="inline-flex rounded-md px-2 py-0.5 text-xs font-medium" :class="statusClass(row.status)">{{ t(`admin.ustcSpeed.status.${row.status}`) }}</span><p v-if="row.reason" class="mt-1 max-w-52 truncate text-[11px] text-gray-500" :title="reasonLabel(row.reason)">{{ reasonLabel(row.reason) }}</p></template>
      <template #cell-overall_tps="{ row }"><span class="font-medium tabular-nums">{{ number(overallTps(row)) }}</span></template>
      <template #cell-generation_tps="{ row }"><span class="tabular-nums">{{ number(row.generation_tps) }}</span></template>
      <template #cell-ttft_ms="{ row }"><span class="whitespace-nowrap tabular-nums">{{ row.ttft_ms == null ? '—' : `${number(row.ttft_ms)} ms` }}</span></template>
      <template #cell-tokens="{ row }"><span class="whitespace-nowrap tabular-nums">{{ row.input_tokens_known ? row.input_tokens.toLocaleString() : '—' }} / {{ row.token_count_known ? row.output_tokens.toLocaleString() : '—' }}</span></template>
      <template #cell-actions="{ row }"><button type="button" class="text-xs font-medium text-primary-600 hover:underline dark:text-primary-300" @click.stop="emit('open', row.id)">{{ t('admin.ustcSpeed.action.showDetails') }}</button></template>
      <template #empty><p class="py-8 text-sm text-gray-500">{{ error ? t('admin.ustcSpeed.error.loadSamples') : t('admin.ustcSpeed.samples.noData') }}</p></template>
    </DataTable>
    <Pagination v-if="total > 0" :page="page" :page-size="pageSize" :page-size-options="[10, 20, 50, 100, 200]" :total="total" @update:page="changePage" @update:page-size="changePageSize" />
  </section>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import DataTable from '@/components/common/DataTable.vue'
import Pagination from '@/components/common/Pagination.vue'
import { adminUSTCSpeedAPI } from '@/api/admin/ustcSpeed'
import type { USTCSpeedQuery, USTCSpeedSample, USTCSpeedTask } from '@/api/admin/ustcSpeed'

const props = defineProps<{ query: USTCSpeedQuery; tasks: USTCSpeedTask[]; refreshVersion: number; reasonLabel: (reason: string) => string }>()
const emit = defineEmits<{ (event: 'open', id: number): void }>()
const { t, locale } = useI18n()
const items = ref<USTCSpeedSample[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(10)
const loading = ref(false)
const error = ref(false)
let sequence = 0
let controller: AbortController | null = null
let disposed = false
const columns = computed(() => [
  ['started_at', 'samples.startedAt'], ['task_id', 'task.title'], ['account_name', 'samples.account'],
  ['model', 'samples.model'], ['status', 'samples.status'], ['overall_tps', 'samples.overallTps'],
  ['generation_tps', 'samples.generationTps'], ['ttft_ms', 'samples.ttft'], ['tokens', 'samples.tokens'], ['actions', 'samples.action'],
].map(([key, label]) => ({ key, label: t(`admin.ustcSpeed.${label}`) })))

function number(value: number | null | undefined) { return value == null || !Number.isFinite(value) ? '—' : value.toLocaleString(locale.value, { maximumFractionDigits: 2 }) }
function timestamp(value: string) {
  try {
    return new Intl.DateTimeFormat(locale.value === 'zh' ? 'zh-CN' : 'en-US', { timeZone: props.query.timezone, month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false }).format(new Date(value))
  } catch { return value }
}
function taskName(id: number | null) { return id == null ? '—' : props.tasks.find((task) => task.id === id)?.name || `#${id}` }
function overallTps(sample: USTCSpeedSample) {
  return sample.overall_tps ?? (sample.token_count_known && sample.total_ms != null && sample.total_ms > 0 ? sample.output_tokens * 1000 / sample.total_ms : null)
}
function statusClass(status: string) {
  return status === 'success' ? 'bg-emerald-50 text-emerald-700 dark:bg-emerald-900/20 dark:text-emerald-300'
    : ['error', 'rate_limited'].includes(status) ? 'bg-amber-50 text-amber-700 dark:bg-amber-900/20 dark:text-amber-300'
      : 'bg-gray-100 text-gray-600 dark:bg-dark-600 dark:text-gray-300'
}
async function load(silent = false) {
  if (disposed) return
  const request = ++sequence
  controller?.abort()
  controller = new AbortController()
  const signal = controller.signal
  loading.value = !silent
  try {
    const result = await adminUSTCSpeedAPI.listSamples({ ...props.query, source: 'probe', limit: pageSize.value, offset: (page.value - 1) * pageSize.value }, { signal })
    if (request !== sequence) return
    items.value = result.items
    total.value = result.total
    error.value = false
    const last = Math.max(1, Math.ceil(result.total / pageSize.value))
    if (page.value > last) { page.value = last; void load(silent) }
  } catch {
    if (request !== sequence || signal.aborted) return
    error.value = true
  } finally {
    if (request === sequence) { loading.value = false; controller = null }
  }
}
function changePage(value: number) { page.value = value; void load() }
function changePageSize(value: number) { pageSize.value = value; page.value = 1; void load() }
watch([() => JSON.stringify(props.query), () => props.refreshVersion], ([query], previous) => {
  const changed = query !== previous?.[0]
  if (changed) { page.value = 1; items.value = []; total.value = 0 }
  void load(!changed)
}, { immediate: true })
onUnmounted(() => { disposed = true; sequence++; controller?.abort() })
</script>
