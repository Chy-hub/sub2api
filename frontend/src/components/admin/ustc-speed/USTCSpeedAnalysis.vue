<template>
  <div v-if="!analysis" class="flex min-h-[380px] items-center justify-center px-6 text-center text-sm text-gray-500">
    {{ t('admin.ustcSpeed.analysis.unavailable') }}
  </div>
  <div v-else class="space-y-5" data-testid="speed-analysis">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <p class="max-w-3xl text-xs leading-5 text-gray-500 dark:text-gray-400">{{ t(`admin.ustcSpeed.analysis.${view}Hint`) }}</p>
      <label class="flex min-w-56 items-center gap-2 text-xs text-gray-500">
        {{ t('admin.ustcSpeed.analysis.subject') }}
        <select v-model="selectedKey" class="min-w-0 flex-1 rounded-lg border border-gray-200 bg-white px-3 py-2 text-sm text-gray-800 dark:border-dark-600 dark:bg-dark-900 dark:text-gray-200" data-testid="analysis-subject">
          <option v-for="row in subjects" :key="keyOf(row)" :value="keyOf(row)">{{ row.model }} · {{ sourceLabel(row.source) }}</option>
        </select>
      </label>
    </div>

    <div class="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
      <div v-for="card in cards" :key="card.label" class="rounded-xl border border-gray-100 bg-gray-50/80 p-4 dark:border-dark-700 dark:bg-dark-900/40">
        <p class="text-xs text-gray-500 dark:text-gray-400">{{ card.label }}</p>
        <p class="mt-2 text-xl font-semibold tabular-nums text-gray-900 dark:text-white">{{ card.value }}</p>
        <p class="mt-1 text-xs" :class="card.tone">{{ card.detail }}</p>
      </div>
    </div>

    <div class="flex flex-wrap gap-x-4 gap-y-1 text-xs text-gray-500 dark:text-gray-400">
      <span>{{ t('admin.ustcSpeed.analysis.currentRange', { range: rangeLabel(analysis.current_start, analysis.current_end) }) }}</span>
      <span v-if="analysis.previous_start && analysis.previous_end">{{ t('admin.ustcSpeed.analysis.previousRange', { range: rangeLabel(analysis.previous_start, analysis.previous_end) }) }}</span>
      <span>{{ t('admin.ustcSpeed.analysis.validSamples', { current: current?.valid_count ?? 0, previous: previous?.valid_count ?? 0 }) }}</span>
    </div>
    <p v-if="!current || current.valid_count < 20 || (analysis.previous_start && (!previous || previous.valid_count < 20))" class="rounded-lg bg-amber-50 px-3 py-2 text-xs leading-5 text-amber-800 dark:bg-amber-900/20 dark:text-amber-200" role="status">{{ t('admin.ustcSpeed.analysis.sampleCaution') }}</p>

    <div v-if="!current?.valid_count && !previous?.valid_count" class="flex h-[300px] items-center justify-center text-sm text-gray-500">{{ t('admin.ustcSpeed.overview.noSeries') }}</div>
    <div v-else class="h-[330px] min-w-0" data-testid="analysis-chart">
      <SpeedChart type="bar" :data="chartData" :options="chartOptions" />
    </div>

    <template v-if="view === 'compare'">
      <div class="overflow-x-auto rounded-xl border border-gray-100 dark:border-dark-700">
        <table class="w-full whitespace-nowrap text-left text-sm">
          <thead class="bg-gray-50 text-xs text-gray-500 dark:bg-dark-900/40"><tr>
            <th class="px-3 py-2.5">{{ t('admin.ustcSpeed.analysis.subject') }}</th>
            <th class="px-3 py-2.5">{{ t('admin.ustcSpeed.analysis.mean') }}</th>
            <th class="px-3 py-2.5">{{ t('admin.ustcSpeed.analysis.previousMean') }}</th>
            <th class="px-3 py-2.5">{{ t('admin.ustcSpeed.analysis.change') }}</th>
            <th class="px-3 py-2.5">{{ slowLabel }}</th>
            <th class="px-3 py-2.5">{{ t('admin.ustcSpeed.analysis.samplePair') }}</th>
          </tr></thead>
          <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
            <tr v-for="subject in subjects" :key="keyOf(subject)" class="cursor-pointer text-gray-700 hover:bg-gray-50 dark:text-gray-300 dark:hover:bg-dark-700/40" :class="keyOf(subject) === selectedKey ? 'bg-primary-50/50 dark:bg-primary-900/10' : ''" @click="selectedKey = keyOf(subject)">
              <td class="px-3 py-3"><button type="button" class="text-left font-medium" @click="selectedKey = keyOf(subject)">{{ subject.model }}<small class="ml-2 font-normal text-gray-400">{{ sourceLabel(subject.source) }}</small></button></td>
              <td class="px-3 py-3 tabular-nums">{{ formatValue(findStats(subject, 'current')?.mean) }}</td>
              <td class="px-3 py-3 tabular-nums">{{ formatValue(findStats(subject, 'previous')?.mean) }}</td>
              <td class="px-3 py-3 tabular-nums" :class="changeTone(changeFor(subject))">{{ formatChange(changeFor(subject)) }}</td>
              <td class="px-3 py-3 tabular-nums">{{ formatValue(slowValue(findStats(subject, 'current'))) }}</td>
              <td class="px-3 py-3 tabular-nums">{{ findStats(subject, 'current')?.valid_count ?? 0 }} / {{ findStats(subject, 'previous')?.valid_count ?? 0 }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <details class="rounded-xl border border-gray-100 dark:border-dark-700">
        <summary class="cursor-pointer px-4 py-3 text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('admin.ustcSpeed.analysis.mixTitle') }}</summary>
        <div class="space-y-3 border-t border-gray-100 p-4 dark:border-dark-700">
          <p class="text-xs leading-5 text-gray-500 dark:text-gray-400">{{ t('admin.ustcSpeed.analysis.mixHint') }}</p>
          <select v-model="lengthDimension" class="rounded-lg border border-gray-200 bg-transparent px-3 py-2 text-sm dark:border-dark-600 dark:text-gray-300">
            <option value="input">{{ t('admin.ustcSpeed.detail.inputTokens') }}</option>
            <option value="output">{{ t('admin.ustcSpeed.detail.outputTokens') }}</option>
          </select>
          <table class="w-full text-left text-xs text-gray-600 dark:text-gray-400">
            <thead><tr><th class="py-2">{{ t('admin.ustcSpeed.analysis.length') }}</th><th>{{ t('admin.ustcSpeed.analysis.currentShare') }}</th><th>{{ t('admin.ustcSpeed.analysis.previousShare') }}</th><th>{{ t('admin.ustcSpeed.analysis.withinChange') }}</th></tr></thead>
            <tbody><tr v-for="row in lengthRows" :key="row.bucket" class="border-t border-gray-100 dark:border-dark-700"><td class="py-2.5">{{ row.label }}</td><td>{{ row.currentShare }}</td><td>{{ row.previousShare }}</td><td :class="changeTone(row.change)">{{ formatChange(row.change) }}</td></tr></tbody>
          </table>
        </div>
      </details>
    </template>
    <p class="text-xs leading-5 text-gray-400 dark:text-gray-500">{{ t('admin.ustcSpeed.analysis.method') }}</p>
  </div>
</template>

<script setup lang="ts">
import { Chart as ChartJS, BarController, BarElement, CategoryScale, Legend, LinearScale, Tooltip } from 'chart.js'
import { Chart as SpeedChart } from 'vue-chartjs'
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { USTCSpeedAggregate, USTCSpeedAnalysis, USTCSpeedAnalysisStats, USTCSpeedMetric } from '@/api/admin/ustcSpeed'

ChartJS.register(BarController, BarElement, CategoryScale, Legend, LinearScale, Tooltip)
const props = defineProps<{ analysis?: USTCSpeedAnalysis; view: 'compare' | 'distribution'; metric: USTCSpeedMetric; aggregate: USTCSpeedAggregate; timezone: string }>()
const { t, locale } = useI18n()
const selectedKey = ref('')
const lengthDimension = ref<'input' | 'output'>('input')
const tps = computed(() => props.metric.endsWith('tps'))
const unit = computed(() => t(`admin.ustcSpeed.metric.${tps.value ? 'unitTps' : 'unitMs'}`))
const slowLabel = computed(() => t(`admin.ustcSpeed.analysis.${tps.value ? 'slowTps' : 'slowLatency'}`))
const keyOf = (row: Pick<USTCSpeedAnalysisStats, 'model' | 'source'>) => JSON.stringify([row.model, row.source])
const sourceLabel = (source: string) => t(`admin.ustcSpeed.source.${source}`)
const subjects = computed(() => {
  const rows = new Map<string, USTCSpeedAnalysisStats>()
  for (const row of props.analysis?.groups ?? []) {
    const key = keyOf(row)
    if (!rows.has(key) || row.period === 'current') rows.set(key, row)
  }
  return [...rows.values()].sort((a, b) => b.valid_count - a.valid_count || a.model.localeCompare(b.model))
})
watch(subjects, (rows) => {
  if (!rows.some((row) => keyOf(row) === selectedKey.value)) selectedKey.value = rows[0] ? keyOf(rows[0]) : ''
}, { immediate: true })
const selected = computed(() => subjects.value.find((row) => keyOf(row) === selectedKey.value))
function findStats(subject: Pick<USTCSpeedAnalysisStats, 'model' | 'source'> | undefined, period: string) {
  return subject && props.analysis?.groups.find((row) => row.model === subject.model && row.source === subject.source && row.period === period)
}
const current = computed(() => findStats(selected.value, 'current'))
const previous = computed(() => findStats(selected.value, 'previous'))
function formatValue(value: number | null | undefined): string {
  return value == null || !Number.isFinite(value) ? '—' : `${value.toLocaleString(locale.value === 'zh' ? 'zh-CN' : 'en-US', { maximumFractionDigits: 2 })} ${unit.value}`
}
function percentChange(a: USTCSpeedAnalysisStats | undefined, b: USTCSpeedAnalysisStats | undefined, field: 'mean' | 'p10' | 'p50' | 'p90' = 'mean'): number | null {
  const minSamples = field === 'p10' || field === 'p90' ? 20 : 5
  if (!a || !b || a.valid_count < minSamples || b.valid_count < minSamples || a[field] == null || b[field] == null || b[field] === 0) return null
  return (a[field]! / b[field]! - 1) * 100
}
function changeFor(subject: USTCSpeedAnalysisStats) { return percentChange(findStats(subject, 'current'), findStats(subject, 'previous')) }
function formatChange(value: number | null) { return value === null ? '—' : `${value > 0 ? '+' : ''}${value.toFixed(1)}%` }
function changeTone(value: number | null) {
  if (value === null || Math.abs(value) < 0.05) return 'text-gray-500 dark:text-gray-400'
  return (tps.value ? value > 0 : value < 0) ? 'text-emerald-600 dark:text-emerald-400' : 'text-amber-600 dark:text-amber-400'
}
function slowValue(row?: USTCSpeedAnalysisStats) { return row && row.valid_count >= 20 ? row[tps.value ? 'p10' : 'p90'] : null }
const cards = computed(() => {
  const count = current.value?.valid_count ?? 0
  const common = { tone: 'text-gray-500 dark:text-gray-400' }
  const meanChange = percentChange(current.value, previous.value)
  const tailChange = percentChange(current.value, previous.value, tps.value ? 'p10' : 'p90')
  const cv = count >= 20 && current.value?.mean && current.value.stddev != null ? current.value.stddev / current.value.mean * 100 : null
  return [
    { label: t('admin.ustcSpeed.analysis.mean'), value: formatValue(current.value?.mean), detail: t('admin.ustcSpeed.analysis.versus', { change: formatChange(meanChange) }), tone: changeTone(meanChange) },
    { ...common, label: t('admin.ustcSpeed.analysis.median'), value: formatValue(count >= 5 ? current.value?.p50 : null), detail: t('admin.ustcSpeed.analysis.medianHint') },
    { label: slowLabel.value, value: formatValue(slowValue(current.value)), detail: t('admin.ustcSpeed.analysis.versus', { change: formatChange(tailChange) }), tone: changeTone(tailChange) },
    { ...common, label: t('admin.ustcSpeed.analysis.variation'), value: cv === null ? '—' : `${cv.toFixed(1)}%`, detail: t('admin.ustcSpeed.analysis.variationHint') },
  ]
})
function rangeLabel(start: string, end: string): string {
  const formatter = new Intl.DateTimeFormat(locale.value === 'zh' ? 'zh-CN' : 'en-US', { timeZone: props.timezone, month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', hourCycle: 'h23' })
  return `${formatter.format(new Date(start))} — ${formatter.format(new Date(end))}`
}
const periods = computed<Array<'current' | 'previous'>>(() => props.analysis?.previous_start ? ['current', 'previous'] : ['current'])
function statistic(row?: USTCSpeedAnalysisStats): number | null {
  const field = props.aggregate === 'mean' ? 'mean' : props.aggregate
  const minimum = field === 'p10' || field === 'p90' ? 20 : field === 'p50' ? 5 : 1
  return row && row.valid_count >= minimum ? row[field] : null
}
const bins = computed(() => {
  const bins = props.analysis?.distribution.filter((row) => keyOf(row) === selectedKey.value) ?? []
  if (!bins.length) return []
  const min = Math.min(...bins.map((row) => row.lower))
  const max = Math.max(...bins.map((row) => row.upper))
  return Array.from({ length: min === max ? 1 : 16 }, (_, index) => ({ lower: min + (max - min) * index / 16, upper: min + (max - min) * (index + 1) / 16 }))
})
const trendLength = computed(() => Math.max(1, ...(props.analysis?.trend.filter((row) => keyOf(row) === selectedKey.value).map((row) => row.bucket + 1) ?? [])))
function chartPoint(period: string, index: number): USTCSpeedAnalysisStats | undefined {
  return props.analysis?.trend.find((row) => keyOf(row) === selectedKey.value && row.period === period && row.bucket === index)
}
const chartData = computed<any>(() => ({
  labels: props.view === 'distribution'
    ? bins.value.map((bin) => `${bin.lower.toFixed(1)}–${bin.upper.toFixed(1)}`)
    : Array.from({ length: trendLength.value }, (_, index) => {
      const minutes = index * (props.analysis?.bucket_seconds ?? 60) / 60
      return minutes >= 1440 ? `${(minutes / 1440).toFixed(1)}d` : `${(minutes / 60).toFixed(1)}h`
    }),
  datasets: periods.value.map((period, index) => ({
    label: t(`admin.ustcSpeed.analysis.${period}`),
    data: props.view === 'distribution' ? bins.value.map((_bin, binIndex) => {
      const bin = props.analysis?.distribution.find((row) => keyOf(row) === selectedKey.value && row.period === period && row.bin === binIndex)
      const total = findStats(selected.value, period)?.valid_count ?? 0
      return total ? (bin?.count ?? 0) * 100 / total : null
    }) : Array.from({ length: trendLength.value }, (_, bucket) => statistic(chartPoint(period, bucket))),
    backgroundColor: index === 0 ? '#3b82f6cc' : '#94a3b866',
    borderColor: index === 0 ? '#3b82f6' : '#94a3b8',
    borderWidth: index === 0 ? 0 : 1,
    borderRadius: 3,
    maxBarThickness: props.view === 'distribution' ? 36 : 12,
  })),
}))
const chartOptions = computed<any>(() => ({
  responsive: true, maintainAspectRatio: false, animation: false,
  interaction: { mode: 'index', intersect: false },
  plugins: {
    legend: { position: 'bottom', labels: { color: '#94a3b8', usePointStyle: true, boxWidth: 8, padding: 20 } },
    tooltip: { callbacks: {
      title: (items: Array<{ dataIndex: number }>) => {
        const index = items[0]?.dataIndex
        if (index === undefined || !props.analysis) return ''
        if (props.view === 'distribution') return `${bins.value[index]?.lower.toFixed(2)}–${bins.value[index]?.upper.toFixed(2)} ${unit.value}`
        const stamps = periods.value.map((period) => {
          const start = period === 'current' ? props.analysis!.current_start : props.analysis!.previous_start
          const end = period === 'current' ? props.analysis!.current_end : props.analysis!.previous_end
          if (!start || !end) return ''
          const from = new Date(Date.parse(start) + index * props.analysis!.bucket_seconds * 1000)
          const to = new Date(Math.min(from.getTime() + props.analysis!.bucket_seconds * 1000, Date.parse(end)))
          return `${t(`admin.ustcSpeed.analysis.${period}`)}: ${rangeLabel(from.toISOString(), to.toISOString())}`
        })
        return stamps.filter(Boolean)
      },
      label: (item: { datasetIndex: number; dataIndex: number; parsed: { y: number | null }; dataset: { label: string } }) => {
        const period = periods.value[item.datasetIndex]
        if (props.view === 'distribution') {
          const row = props.analysis?.distribution.find((row) => keyOf(row) === selectedKey.value && row.period === period && row.bin === item.dataIndex)
          return `${item.dataset.label}: ${item.parsed.y?.toFixed(1) ?? '—'}% · n=${row?.count ?? 0}`
        }
        return `${item.dataset.label}: ${formatValue(item.parsed.y)} · n=${chartPoint(period, item.dataIndex)?.valid_count ?? 0}`
      },
    } },
  },
  scales: {
    x: { grid: { display: false }, ticks: { color: '#94a3b8', maxTicksLimit: 8, maxRotation: 0 }, title: { display: true, text: props.view === 'distribution' ? unit.value : t('admin.ustcSpeed.analysis.elapsedAxis'), color: '#94a3b8' } },
    y: { beginAtZero: true, grid: { color: '#94a3b81a' }, ticks: { color: '#94a3b8' }, title: { display: true, text: props.view === 'distribution' ? t('admin.ustcSpeed.analysis.shareAxis') : unit.value, color: '#94a3b8' } },
  },
}))
const lengthRows = computed(() => {
  const rows = props.analysis?.lengths.filter((row) => keyOf(row) === selectedKey.value && row.dimension === lengthDimension.value) ?? []
  const labels = lengthDimension.value === 'input' ? ['0–1K', '1K–4K', '4K–16K', '16K–64K', '64K+'] : ['0–128', '128–512', '512–2K', '2K–8K', '8K+']
  return [-1, 0, 1, 2, 3, 4].filter((bucket) => rows.some((row) => row.bucket === bucket)).map((bucket) => {
    const a = rows.find((row) => row.period === 'current' && row.bucket === bucket)
    const b = rows.find((row) => row.period === 'previous' && row.bucket === bucket)
    const share = (n: number, total: number) => total ? `${(n * 100 / total).toFixed(1)}% · n=${n}` : '—'
    return { bucket, label: bucket < 0 ? t('admin.ustcSpeed.filter.reasoningUnknown') : labels[bucket], currentShare: share(a?.valid_count ?? 0, current.value?.valid_count ?? 0), previousShare: share(b?.valid_count ?? 0, previous.value?.valid_count ?? 0), change: percentChange(a, b) }
  })
})
</script>
