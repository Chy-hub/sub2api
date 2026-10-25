<template>
  <BaseDialog :show="show" :title="t('admin.ustcSpeed.detail.title')" width="wide" @close="emit('close')">
    <div v-if="loading" class="flex min-h-48 items-center justify-center text-sm text-gray-500">
      {{ t('admin.ustcSpeed.detail.loading') }}
    </div>
    <div v-else-if="sample" class="space-y-5">
      <div v-if="isLegacy" class="rounded-xl border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-900 dark:border-amber-900/50 dark:bg-amber-900/20 dark:text-amber-100">
        {{ t('admin.ustcSpeed.detail.legacyNotice') }}
      </div>
      <div v-else class="rounded-xl border border-sky-200 bg-sky-50 px-4 py-3 text-sm text-sky-900 dark:border-sky-900/50 dark:bg-sky-900/20 dark:text-sky-100">
        {{ t('admin.ustcSpeed.detail.upstreamTimingNotice') }}
      </div>

      <section class="space-y-3">
        <h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.ustcSpeed.detail.overview') }}</h3>
        <dl class="grid gap-3 rounded-xl border border-gray-200 p-4 text-sm dark:border-dark-700 sm:grid-cols-2 lg:grid-cols-3">
          <InfoField :label="t('admin.ustcSpeed.samples.model')" :value="sample.model" />
          <InfoField :label="t('admin.ustcSpeed.samples.account')" :value="`${sample.account_name} (#${sample.account_id})`" />
          <InfoField :label="t('admin.ustcSpeed.samples.source')" :value="sourceLabel(sample.source)" />
          <InfoField :label="t('admin.ustcSpeed.samples.status')" :value="statusLabel(sample.status)" />
          <InfoField :label="t('admin.ustcSpeed.detail.startedAt')" :value="formatDateTime(sample.started_at)" />
          <InfoField :label="t('admin.ustcSpeed.detail.finishedAt')" :value="formatDateTime(sample.finished_at)" />
          <InfoField :label="t('admin.ustcSpeed.detail.task')" :value="sample.task_id === null ? t('admin.ustcSpeed.detail.notAvailable') : String(sample.task_id)" />
          <InfoField :label="t('admin.ustcSpeed.detail.round')" :value="sample.round_id || t('admin.ustcSpeed.detail.notAvailable')" />
          <InfoField :label="t('admin.ustcSpeed.samples.profile')" :value="sample.profile || t('admin.ustcSpeed.detail.notAvailable')" />
          <InfoField :label="t('admin.ustcSpeed.samples.reasoning')" :value="sample.reasoning || t('admin.ustcSpeed.detail.notObserved')" />
          <InfoField :label="t('admin.ustcSpeed.samples.stream')" :value="sample.stream ? t('admin.ustcSpeed.filter.streaming') : t('admin.ustcSpeed.filter.nonStreaming')" />
          <InfoField :label="t('admin.ustcSpeed.detail.apiMode')" :value="sample.api_mode || t('admin.ustcSpeed.detail.notObserved')" />
          <InfoField :label="t('admin.ustcSpeed.detail.measurementMode')" :value="sample.measurement_mode || t('admin.ustcSpeed.detail.notObserved')" />
          <InfoField :label="t('admin.ustcSpeed.detail.httpStatus')" :value="sample.http_status ? String(sample.http_status) : t('admin.ustcSpeed.detail.notObserved')" />
          <InfoField :label="t('admin.ustcSpeed.detail.observedModel')" :value="sample.observed_model || t('admin.ustcSpeed.detail.notObserved')" />
          <InfoField :label="t('admin.ustcSpeed.detail.finishReason')" :value="sample.finish_reason || t('admin.ustcSpeed.detail.notObserved')" />
          <InfoField :label="t('admin.ustcSpeed.detail.reason')" :value="sample.reason || t('admin.ustcSpeed.detail.notAvailable')" />
        </dl>
      </section>

      <section class="space-y-3">
        <h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.ustcSpeed.detail.metrics') }}</h3>
        <dl class="grid gap-3 rounded-xl border border-gray-200 p-4 text-sm dark:border-dark-700 sm:grid-cols-2 lg:grid-cols-3">
          <InfoField :label="t('admin.ustcSpeed.metric.headersMs')" :value="formatMilliseconds(sample.headers_ms)" />
          <InfoField :label="t('admin.ustcSpeed.metric.firstOutputMs')" :value="formatMilliseconds(sample.first_output_ms)" />
          <InfoField :label="t('admin.ustcSpeed.metric.ttftMs')" :value="formatMilliseconds(sample.ttft_ms)" />
          <InfoField :label="t('admin.ustcSpeed.metric.outputMs')" :value="formatMilliseconds(sample.output_ms)" />
          <InfoField :label="t('admin.ustcSpeed.metric.totalMs')" :value="formatMilliseconds(sample.total_ms)" />
          <InfoField :label="t('admin.ustcSpeed.metric.tailMs')" :value="formatMilliseconds(sample.tail_ms)" />
          <InfoField :label="t('admin.ustcSpeed.metric.generationTps')" :value="formatRate(sample.generation_tps)" />
          <InfoField :label="t('admin.ustcSpeed.metric.overallTps')" :value="formatRate(sample.overall_tps)" />
          <InfoField :label="t('admin.ustcSpeed.metric.maxGapMs')" :value="formatMilliseconds(sample.max_gap_ms)" />
          <InfoField :label="t('admin.ustcSpeed.detail.inputTokens')" :value="formatKnownInteger(sample.input_tokens, sample.input_tokens_known)" />
          <InfoField :label="t('admin.ustcSpeed.detail.outputTokens')" :value="formatKnownInteger(sample.output_tokens, sample.token_count_known)" />
          <InfoField :label="t('admin.ustcSpeed.detail.reasoningTokens')" :value="formatKnownInteger(sample.reasoning_tokens, sample.reasoning_tokens_known)" />
          <InfoField :label="t('admin.ustcSpeed.detail.cachedTokens')" :value="formatKnownInteger(sample.cached_tokens, sample.cached_tokens_known)" />
          <InfoField :label="t('admin.ustcSpeed.detail.inputTokenCountKnown')" :value="formatKnownFlag(sample.input_tokens_known)" />
          <InfoField :label="t('admin.ustcSpeed.detail.outputTokenCountKnown')" :value="formatKnownFlag(sample.token_count_known)" />
          <InfoField :label="t('admin.ustcSpeed.detail.events')" :value="formatInteger(sample.event_count)" />
          <InfoField :label="t('admin.ustcSpeed.detail.chars')" :value="formatInteger(sample.output_chars)" />
          <InfoField :label="t('admin.ustcSpeed.detail.reasoningChars')" :value="formatInteger(sample.reasoning_chars)" />
          <InfoField :label="t('admin.ustcSpeed.detail.gapOver1s')" :value="formatInteger(sample.gap_over_1s)" />
          <InfoField :label="t('admin.ustcSpeed.detail.gapOver2s')" :value="formatInteger(sample.gap_over_2s)" />
          <InfoField :label="t('admin.ustcSpeed.detail.errorCode')" :value="sample.error_code || t('admin.ustcSpeed.detail.notAvailable')" />
          <InfoField :label="t('admin.ustcSpeed.detail.streamDone')" :value="sample.stream_done ? t('admin.ustcSpeed.detail.yes') : t('admin.ustcSpeed.detail.no')" />
          <InfoField :label="t('admin.ustcSpeed.detail.providerCost')" :value="sample.provider_cost === null ? t('admin.ustcSpeed.detail.notObserved') : String(sample.provider_cost)" />
        </dl>
      </section>
    </div>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, defineComponent, h } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import type { USTCSpeedSample } from '@/api/admin/ustcSpeed'

const props = defineProps<{ show: boolean; loading: boolean; sample: USTCSpeedSample | null }>()
const emit = defineEmits<{ (event: 'close'): void }>()
const { t, locale } = useI18n()

const isLegacy = computed(() => props.sample?.measurement_mode === 'legacy_usage' || props.sample?.profile === 'legacy')

function formatDateTime(value: string | null | undefined): string {
  if (!value) return t('admin.ustcSpeed.detail.notObserved')
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return t('admin.ustcSpeed.detail.notObserved')
  return new Intl.DateTimeFormat(locale.value === 'zh' ? 'zh-CN' : 'en-US', { dateStyle: 'medium', timeStyle: 'medium' }).format(date)
}

function formatMilliseconds(value: number | null): string {
  return value === null || !Number.isFinite(value) ? t('admin.ustcSpeed.detail.notObserved') : `${value.toFixed(2)} ${t('admin.ustcSpeed.unit.milliseconds')}`
}

function formatRate(value: number | null): string {
  return value === null || !Number.isFinite(value) ? t('admin.ustcSpeed.detail.notObserved') : `${value.toFixed(2)} ${t('admin.ustcSpeed.unit.tokensPerSecond')}`
}

function formatInteger(value: number | null | undefined): string {
  return value === null || value === undefined || !Number.isFinite(value)
    ? t('admin.ustcSpeed.detail.notObserved')
    : new Intl.NumberFormat(locale.value === 'zh' ? 'zh-CN' : 'en-US').format(value)
}

function formatKnownInteger(value: number | null | undefined, known: boolean | undefined): string {
  return known === true ? formatInteger(value) : t('admin.ustcSpeed.detail.notObserved')
}

function formatKnownFlag(known: boolean | undefined): string {
  return known === undefined
    ? t('admin.ustcSpeed.detail.notObserved')
    : t(known ? 'admin.ustcSpeed.detail.yes' : 'admin.ustcSpeed.detail.no')
}

function sourceLabel(source: string): string {
  if (source === 'real') return t('admin.ustcSpeed.source.real')
  if (source === 'probe') return t('admin.ustcSpeed.source.probe')
  return source || t('admin.ustcSpeed.detail.notObserved')
}

function statusLabel(status: string): string {
  const known = ['success', 'error', 'rate_limited', 'skipped', 'cancelled', 'recorded', 'queued', 'paused']
  return known.includes(status) ? t(`admin.ustcSpeed.status.${status}`) : status || t('admin.ustcSpeed.status.unknown')
}

const InfoField = defineComponent({
  props: { label: { type: String, required: true }, value: { type: String, required: true } },
  setup(fieldProps) {
    return () => h('div', { class: 'min-w-0' }, [
      h('dt', { class: 'text-xs text-gray-500 dark:text-gray-400' }, fieldProps.label),
      h('dd', { class: 'mt-1 break-words font-medium text-gray-900 dark:text-gray-100' }, fieldProps.value),
    ])
  },
})
</script>
