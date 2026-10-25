<template>
  <AppLayout>
    <div class="w-full space-y-5">
      <header class="flex flex-wrap items-start justify-between gap-4">
        <div>
          <div class="mb-1 text-[11px] font-semibold uppercase tracking-[0.18em] text-primary-600 dark:text-primary-400">USTC OBSERVATORY</div>
          <h1 class="text-2xl font-bold tracking-tight text-gray-900 dark:text-white">{{ pageTitle }}</h1>
          <p class="mt-1 max-w-3xl text-sm text-gray-500 dark:text-gray-400">{{ pageDescription }}</p>
        </div>
        <div class="flex flex-wrap gap-2">
          <template v-if="isOverview || isSamples">
            <button type="button" class="btn btn-secondary" :disabled="isSamples ? samplesLoading : loading" @click="refreshCurrent">
              {{ (isSamples ? samplesLoading : loading) ? t('admin.ustcSpeed.action.loading') : t('admin.ustcSpeed.action.refresh') }}
            </button>
            <button type="button" class="btn btn-secondary" :disabled="exporting" @click="exportSamples('csv')">
              {{ exporting ? t('admin.ustcSpeed.action.exporting') : t('admin.ustcSpeed.action.exportCsv') }}
            </button>
            <button type="button" class="btn btn-secondary" :disabled="exporting" @click="exportSamples('json')">
              {{ t('admin.ustcSpeed.action.exportJson') }}
            </button>
          </template>
          <button v-if="isTasks" type="button" class="btn btn-primary" @click="openTaskDialog()">
            {{ t('admin.ustcSpeed.action.addTask') }}
          </button>
        </div>
      </header>

      <section v-if="!isTasks" class="rounded-2xl border border-gray-200 bg-white p-4 shadow-sm dark:border-dark-700 dark:bg-dark-800">
        <div class="flex flex-wrap items-end gap-3">
          <label class="min-w-[190px] flex-1 space-y-1.5">
            <span class="text-xs font-semibold text-gray-500 dark:text-gray-400">{{ t('admin.ustcSpeed.source.label') }}</span>
            <select v-model="filters.source" class="filter-input">
              <option value="real">{{ t('admin.ustcSpeed.source.real') }}</option>
              <option value="probe">{{ t('admin.ustcSpeed.source.probe') }}</option>
              <option value="all">{{ t('admin.ustcSpeed.source.all') }}</option>
            </select>
          </label>
          <label class="min-w-[170px] flex-1 space-y-1.5">
            <span class="text-xs font-semibold text-gray-500 dark:text-gray-400">{{ t('admin.ustcSpeed.filter.model') }}</span>
            <select v-model="filters.model" class="filter-input">
              <option value="">{{ t('admin.ustcSpeed.filter.allModels') }}</option>
              <option v-for="model in modelOptions" :key="model" :value="model">{{ model }}</option>
            </select>
          </label>
          <div class="min-w-[190px] flex-1 space-y-1.5">
            <span class="block text-xs font-semibold text-gray-500 dark:text-gray-400">{{ t('admin.ustcSpeed.filter.account') }}</span>
            <details class="relative">
              <summary class="filter-input flex cursor-pointer list-none items-center justify-between gap-2">
                <span class="truncate">{{ accountFilterLabel }}</span><span class="text-gray-400">⌄</span>
              </summary>
              <div class="absolute left-0 top-full z-30 mt-1 max-h-64 w-full min-w-64 overflow-y-auto rounded-xl border border-gray-200 bg-white p-2 shadow-xl dark:border-dark-600 dark:bg-dark-800">
                <button type="button" class="mb-1 w-full rounded-md px-2 py-1.5 text-left text-xs text-primary-600 hover:bg-gray-50 dark:text-primary-300 dark:hover:bg-dark-700" @click="filters.accountIds = []">
                  {{ t('admin.ustcSpeed.filter.allAccounts') }}
                </button>
                <label v-for="account in dimensions.accounts" :key="account.id" class="flex items-center gap-2 rounded-md px-2 py-1.5 text-sm hover:bg-gray-50 dark:hover:bg-dark-700">
                  <input v-model="filters.accountIds" type="checkbox" :value="account.id" class="rounded border-gray-300 text-primary-600 focus:ring-primary-500" />
                  <span class="min-w-0 flex-1 truncate text-gray-700 dark:text-gray-200">{{ account.name }}</span>
                  <span class="text-xs text-gray-400">#{{ account.id }}</span>
                </label>
              </div>
            </details>
          </div>
          <div class="min-w-[235px] space-y-1.5">
            <span class="block text-xs font-semibold text-gray-500 dark:text-gray-400">{{ t('admin.ustcSpeed.filter.timeRange') }}</span>
            <DateRangePicker v-model:start-date="startDate" v-model:end-date="endDate" @change="applyFilters" />
          </div>
          <button type="button" class="btn btn-primary h-[42px]" :disabled="loading" @click="applyFilters">
            {{ t('admin.ustcSpeed.action.apply') }}
          </button>
        </div>

        <details class="mt-4 border-t border-gray-100 pt-3 dark:border-dark-700">
          <summary class="cursor-pointer select-none text-sm font-medium text-gray-600 dark:text-gray-300">{{ t('admin.ustcSpeed.filter.advanced') }}</summary>
          <div class="mt-3 grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
            <label class="filter-field">
              <span>{{ t('admin.ustcSpeed.filter.reasoning') }}</span>
              <select v-model="filters.reasoning" class="filter-input">
                <option value="all">{{ t('admin.ustcSpeed.filter.all') }}</option>
                <option value="on">{{ t('admin.ustcSpeed.filter.reasoningOn') }}</option>
                <option value="off">{{ t('admin.ustcSpeed.filter.reasoningOff') }}</option>
                <option value="unknown">{{ t('admin.ustcSpeed.filter.reasoningUnknown') }}</option>
              </select>
            </label>
            <label class="filter-field">
              <span>{{ t('admin.ustcSpeed.filter.stream') }}</span>
              <select v-model="filters.stream" class="filter-input">
                <option value="all">{{ t('admin.ustcSpeed.filter.all') }}</option>
                <option value="true">{{ t('admin.ustcSpeed.filter.streaming') }}</option>
                <option value="false">{{ t('admin.ustcSpeed.filter.nonStreaming') }}</option>
              </select>
            </label>
            <label class="filter-field">
              <span>{{ t('admin.ustcSpeed.filter.status') }}</span>
              <select v-model="filters.status" class="filter-input">
                <option value="">{{ t('admin.ustcSpeed.filter.anyStatus') }}</option>
                <option v-for="status in statuses" :key="status" :value="status">{{ statusLabel(status) }}</option>
              </select>
            </label>
            <label class="filter-field">
              <span>{{ t('admin.ustcSpeed.filter.profile') }}</span>
              <input v-model.trim="filters.profile" class="filter-input" :placeholder="t('admin.ustcSpeed.filter.profilePlaceholder')" />
            </label>
            <label class="filter-field">
              <span>{{ t('admin.ustcSpeed.filter.taskId') }}</span>
              <input v-model.number="filters.taskId" type="number" min="1" class="filter-input" />
            </label>
            <p class="text-xs text-gray-500 dark:text-gray-400 md:col-span-4">{{ t('admin.ustcSpeed.filter.taskIdHint') }}</p>
            <label class="filter-field">
              <span>{{ t('admin.ustcSpeed.filter.inputMin') }}</span>
              <input v-model.number="filters.inputMin" type="number" min="0" class="filter-input" />
            </label>
            <label class="filter-field">
              <span>{{ t('admin.ustcSpeed.filter.inputMax') }}</span>
              <input v-model.number="filters.inputMax" type="number" min="0" class="filter-input" />
            </label>
            <label class="filter-field">
              <span>{{ t('admin.ustcSpeed.filter.outputMin') }}</span>
              <input v-model.number="filters.outputMin" type="number" min="0" class="filter-input" />
            </label>
            <label class="filter-field">
              <span>{{ t('admin.ustcSpeed.filter.outputMax') }}</span>
              <input v-model.number="filters.outputMax" type="number" min="0" class="filter-input" />
            </label>
            <label class="filter-field">
              <span>{{ t('admin.ustcSpeed.interval.label') }}</span>
              <select v-model="filters.interval" class="filter-input">
                <option value="auto">{{ t('admin.ustcSpeed.interval.auto') }}</option>
                <option value="raw">{{ t('admin.ustcSpeed.interval.raw') }}</option>
                <option value="5m">{{ t('admin.ustcSpeed.interval.m5') }}</option>
                <option value="15m">{{ t('admin.ustcSpeed.interval.m15') }}</option>
                <option value="1h">{{ t('admin.ustcSpeed.interval.h1') }}</option>
                <option value="1d">{{ t('admin.ustcSpeed.interval.d1') }}</option>
              </select>
            </label>
            <label class="filter-field">
              <span>{{ t('admin.ustcSpeed.aggregate.label') }}</span>
              <select v-model="filters.aggregate" class="filter-input">
                <option value="p50">{{ t('admin.ustcSpeed.aggregate.p50') }}</option>
                <option value="mean">{{ t('admin.ustcSpeed.aggregate.mean') }}</option>
                <option value="p10">{{ t('admin.ustcSpeed.aggregate.p10') }}</option>
                <option value="p90">{{ t('admin.ustcSpeed.aggregate.p90') }}</option>
              </select>
            </label>
            <label class="filter-field">
              <span>{{ t('admin.ustcSpeed.filter.timezone') }}</span>
              <input v-model.trim="filters.timezone" class="filter-input" />
            </label>
          </div>
        </details>
      </section>

      <p v-if="!isTasks && settingsLoaded && settings.retention_days > 0" class="rounded-lg border border-gray-200 bg-gray-50 px-3 py-2 text-xs text-gray-500 dark:border-dark-700 dark:bg-dark-800 dark:text-gray-400">
        {{ t('admin.ustcSpeed.samples.retentionNotice', { days: settings.retention_days }) }}
      </p>

      <template v-if="isOverview">
        <div v-if="hasLegacy" class="rounded-xl border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-900 dark:border-amber-900/50 dark:bg-amber-900/20 dark:text-amber-100">
          {{ t('admin.ustcSpeed.overview.legacyNotice') }}
        </div>
        <div v-if="overview?.truncated || (overview?.dropped_samples ?? 0) > 0" class="rounded-xl border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-900 dark:border-amber-900/50 dark:bg-amber-900/20 dark:text-amber-100">
          <span v-if="overview?.truncated">{{ t('admin.ustcSpeed.overview.truncated') }}</span>
          <span v-if="overview?.truncated && (overview?.dropped_samples ?? 0) > 0"> </span>
          <span v-if="(overview?.dropped_samples ?? 0) > 0">{{ t('admin.ustcSpeed.overview.dropped', { count: overview?.dropped_samples }) }}</span>
        </div>
        <div v-if="overview?.fallback_applied" role="status" class="rounded-xl border border-sky-200 bg-sky-50 px-4 py-3 text-sm text-sky-900 dark:border-sky-900/50 dark:bg-sky-900/20 dark:text-sky-100">
          {{ t('admin.ustcSpeed.overview.fallbackApplied', { minutes: Math.ceil(overview.fallback_window_seconds / 60) }) }}
        </div>

        <section class="overflow-hidden rounded-2xl border border-gray-200 bg-white shadow-sm dark:border-dark-700 dark:bg-dark-800">
          <div class="flex flex-wrap items-end justify-between gap-4 border-b border-gray-100 px-5 py-4 dark:border-dark-700">
            <div>
              <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.ustcSpeed.overview.chartTitle') }}</h2>
              <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ustcSpeed.overview.sourceNote') }}</p>
            </div>
            <div class="flex flex-wrap items-end gap-2">
              <label class="filter-field min-w-36">
                <span>{{ t('admin.ustcSpeed.metric.view') }}</span>
                <select v-model="chartGroup" class="filter-input" @change="handleChartGroupChange">
                  <option value="tps">{{ t('admin.ustcSpeed.metric.tps') }}</option>
                  <option value="ttft">{{ t('admin.ustcSpeed.metric.ttft') }}</option>
                  <option value="time">{{ t('admin.ustcSpeed.metric.time') }}</option>
                </select>
              </label>
              <label class="filter-field min-w-48">
                <span>{{ t('admin.ustcSpeed.metric.metric') }}</span>
                <select v-model="metric" class="filter-input" @change="loadOverview">
                  <option v-for="option in metricOptions" :key="option.value" :value="option.value">{{ option.label }}</option>
                </select>
              </label>
              <label class="filter-field min-w-44">
                <span>{{ t('admin.ustcSpeed.accountView.label') }}</span>
                <select v-model="accountView" class="filter-input">
                  <option value="accounts">{{ t('admin.ustcSpeed.accountView.accounts') }}</option>
                  <option value="equal">{{ t('admin.ustcSpeed.accountView.equal') }}</option>
                </select>
              </label>
            </div>
          </div>
          <div class="px-3 py-3 sm:px-5 sm:py-4">
            <div v-if="loading" class="flex h-[420px] items-center justify-center text-sm text-gray-500">{{ t('admin.ustcSpeed.action.loading') }}</div>
            <div v-else-if="equalAverageNeedsAggregation" class="flex min-h-[340px] flex-col items-center justify-center px-6 text-center">
              <p class="max-w-xl text-sm text-amber-800 dark:text-amber-200">{{ t('admin.ustcSpeed.accountView.aggregateRequired') }}</p>
              <button type="button" class="btn btn-secondary mt-3" @click="useFiveMinuteAggregation">{{ t('admin.ustcSpeed.accountView.useFiveMinutes') }}</button>
            </div>
            <div v-else-if="!chartData || !chartData.datasets.length" class="flex min-h-[340px] flex-col items-center justify-center px-6 text-center">
              <div class="mb-2 flex h-12 w-12 items-center justify-center rounded-full bg-gray-100 text-xl text-gray-400 dark:bg-dark-700">∅</div>
              <p class="font-medium text-gray-700 dark:text-gray-200">{{ t('admin.ustcSpeed.overview.noSeries') }}</p>
              <p class="mt-1 max-w-lg text-sm text-gray-500 dark:text-gray-400">{{ t('admin.ustcSpeed.overview.noSeriesHint') }}</p>
            </div>
            <div v-else class="h-[420px] min-w-0">
              <Line :data="chartData" :options="chartOptions" />
            </div>
            <p class="mt-2 text-xs text-gray-400 dark:text-gray-500">{{ t('admin.ustcSpeed.overview.chartHint') }}</p>
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ustcSpeed.overview.timingBasis') }}</p>
          </div>
          <div class="grid gap-3 border-t border-gray-100 bg-gray-50/70 px-5 py-3 text-sm dark:border-dark-700 dark:bg-dark-900/30 sm:grid-cols-2 lg:grid-cols-3 2xl:grid-cols-6">
            <SummaryValue :label="t('admin.ustcSpeed.overview.samples')" :value="formatNumber(overview?.summary.samples)" />
            <SummaryValue :label="t('admin.ustcSpeed.overview.successful')" :value="formatNumber(overview?.summary.successful)" />
            <SummaryValue :label="t('admin.ustcSpeed.overview.rateLimited')" :value="formatNumber(overview?.summary.rate_limited)" />
            <SummaryValue :label="t('admin.ustcSpeed.overview.realSamples')" :value="formatNumber(overview?.summary.real_samples)" />
            <SummaryValue :label="t('admin.ustcSpeed.overview.probeSamples')" :value="formatNumber(overview?.summary.probe_samples)" />
            <SummaryValue :label="t('admin.ustcSpeed.overview.outputTokens')" :value="formatNumber(overview?.summary.output_tokens)" />
          </div>
        </section>

        <details class="rounded-2xl border border-gray-200 bg-white shadow-sm dark:border-dark-700 dark:bg-dark-800">
          <summary class="flex cursor-pointer list-none items-center justify-between gap-3 px-5 py-4">
            <span>
              <strong class="block text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.ustcSpeed.overview.auxiliary') }}</strong>
              <small class="mt-0.5 block text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ustcSpeed.overview.auxiliaryDescription') }}</small>
            </span>
            <span class="text-gray-400">⌄</span>
          </summary>
          <div class="grid gap-4 border-t border-gray-100 p-5 dark:border-dark-700 md:grid-cols-3">
            <SummaryValue :label="t('admin.ustcSpeed.overview.realLatest')" :value="formatAge(overview?.summary.latest_real_at)" />
            <SummaryValue :label="t('admin.ustcSpeed.overview.probeLatest')" :value="formatAge(overview?.summary.latest_probe_at)" />
            <SummaryValue :label="t('admin.ustcSpeed.overview.validPoints')" :value="formatNumber(validMetricPointCount)" />
            <SummaryValue :label="t('admin.ustcSpeed.overview.failed')" :value="formatNumber(overview?.summary.failed)" />
            <SummaryValue :label="t('admin.ustcSpeed.overview.skipped')" :value="formatNumber(overview?.summary.skipped)" />
            <SummaryValue :label="t('admin.ustcSpeed.overview.cancelled')" :value="formatNumber(overview?.summary.cancelled)" />
            <div class="md:col-span-3">
              <div class="mb-2 text-xs font-semibold uppercase tracking-wide text-gray-500 dark:text-gray-400">{{ t('admin.ustcSpeed.overview.seriesBreakdown') }}</div>
              <div v-if="!seriesBreakdown.length" class="text-sm text-gray-500">{{ t('admin.ustcSpeed.overview.noSeries') }}</div>
              <div v-else class="overflow-x-auto rounded-xl border border-gray-200 dark:border-dark-700">
                <table class="min-w-full divide-y divide-gray-100 text-left text-sm dark:divide-dark-700">
                  <thead class="bg-gray-50 text-xs uppercase tracking-wide text-gray-500 dark:bg-dark-900/40 dark:text-gray-400">
                    <tr><th class="px-3 py-2">{{ t('admin.ustcSpeed.samples.model') }}</th><th class="px-3 py-2">{{ t('admin.ustcSpeed.overview.profile') }}</th><th class="px-3 py-2">{{ t('admin.ustcSpeed.overview.source') }}</th><th class="px-3 py-2">{{ t('admin.ustcSpeed.samples.account') }}</th><th class="px-3 py-2">n</th><th class="px-3 py-2">{{ t('admin.ustcSpeed.samples.status') }}</th></tr>
                  </thead>
                  <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
                    <tr v-for="series in seriesBreakdown" :key="series.key" class="text-gray-700 dark:text-gray-300">
                      <td class="px-3 py-2">{{ series.model }}</td><td class="px-3 py-2">{{ series.profile || '—' }}</td><td class="px-3 py-2">{{ sourceLabel(series.source) }}</td><td class="px-3 py-2">{{ series.account }}</td><td class="px-3 py-2">{{ formatNumber(series.count) }}</td><td class="px-3 py-2">{{ formatNumber(series.valid) }} / {{ formatNumber(series.count) }}</td>
                    </tr>
                  </tbody>
                </table>
              </div>
            </div>
          </div>
        </details>
      </template>

      <template v-else-if="isSamples">
        <section class="overflow-hidden rounded-2xl border border-gray-200 bg-white shadow-sm dark:border-dark-700 dark:bg-dark-800">
          <div class="flex flex-wrap items-center justify-between gap-3 border-b border-gray-100 px-5 py-4 dark:border-dark-700">
            <div>
              <h2 class="font-semibold text-gray-900 dark:text-white">{{ t('admin.ustcSpeed.samples.tableTitle') }}</h2>
              <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ustcSpeed.samples.total', { count: formatNumber(sampleTotal) }) }}</p>
            </div>
            <label class="flex items-center gap-2 text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.ustcSpeed.action.pageSize') }}
              <select v-model.number="pageSize" class="filter-input !w-auto" @change="changePageSize">
                <option :value="25">25</option><option :value="50">50</option><option :value="100">100</option>
              </select>
            </label>
          </div>
          <div v-if="samplesLoading" class="flex min-h-56 items-center justify-center text-sm text-gray-500">{{ t('admin.ustcSpeed.samples.loading') }}</div>
          <div v-else-if="!samples.length" class="flex min-h-56 items-center justify-center px-6 text-sm text-gray-500">{{ t('admin.ustcSpeed.samples.noData') }}</div>
          <div v-else class="overflow-x-auto">
            <table class="min-w-[1120px] w-full divide-y divide-gray-100 text-left text-sm dark:divide-dark-700">
              <thead class="bg-gray-50 text-xs font-semibold uppercase tracking-wide text-gray-500 dark:bg-dark-900/40 dark:text-gray-400">
                <tr>
                  <th class="px-4 py-3">{{ t('admin.ustcSpeed.samples.model') }} / {{ t('admin.ustcSpeed.samples.profile') }}</th>
                  <th class="px-4 py-3">{{ t('admin.ustcSpeed.samples.startedAt') }}</th>
                  <th class="px-4 py-3">{{ t('admin.ustcSpeed.samples.account') }}</th>
                  <th class="px-4 py-3">{{ t('admin.ustcSpeed.samples.source') }}</th>
                  <th class="px-4 py-3">{{ t('admin.ustcSpeed.samples.generationTps') }}</th>
                  <th class="px-4 py-3">{{ t('admin.ustcSpeed.samples.ttft') }}</th>
                  <th class="px-4 py-3">{{ t('admin.ustcSpeed.samples.duration') }}</th>
                  <th class="px-4 py-3">{{ t('admin.ustcSpeed.samples.tokens') }}</th>
                  <th class="px-4 py-3">{{ t('admin.ustcSpeed.samples.status') }}</th>
                  <th class="px-4 py-3">{{ t('admin.ustcSpeed.samples.action') }}</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
                <tr v-for="sample in samples" :key="sample.id" class="hover:bg-gray-50/80 dark:hover:bg-dark-700/40">
                  <td class="px-4 py-3"><span class="block font-medium text-gray-900 dark:text-gray-100">{{ sample.model }}</span><span class="text-xs text-gray-500">{{ sample.profile || '—' }}</span></td>
                  <td class="whitespace-nowrap px-4 py-3 text-gray-600 dark:text-gray-300">{{ formatDateTime(sample.started_at) }}</td>
                  <td class="px-4 py-3 text-gray-700 dark:text-gray-300">{{ sample.account_name }}</td>
                  <td class="px-4 py-3"><span :class="sourceClass(sample.source)">{{ sourceLabel(sample.source) }}</span></td>
                  <td class="px-4 py-3 font-medium text-gray-800 dark:text-gray-200">{{ formatRate(sample.generation_tps) }}</td>
                  <td class="px-4 py-3 text-gray-700 dark:text-gray-300">{{ formatMilliseconds(sample.ttft_ms) }}</td>
                  <td class="px-4 py-3 text-gray-700 dark:text-gray-300">{{ formatMilliseconds(sample.total_ms) }}</td>
                  <td class="whitespace-nowrap px-4 py-3 text-gray-700 dark:text-gray-300">{{ formatKnownTokenCount(sample.input_tokens, sample.input_tokens_known) }} / {{ formatKnownTokenCount(sample.output_tokens, sample.token_count_known) }}</td>
                  <td class="px-4 py-3"><span :class="statusClass(sample.status)">{{ statusLabel(sample.status) }}</span></td>
                  <td class="px-4 py-3"><button type="button" class="text-sm font-medium text-primary-600 hover:text-primary-700 dark:text-primary-300" @click="openSample(sample.id)">{{ t('admin.ustcSpeed.action.showDetails') }}</button></td>
                </tr>
              </tbody>
            </table>
          </div>
          <div v-if="sampleTotal > 0" class="flex flex-wrap items-center justify-between gap-3 border-t border-gray-100 px-5 py-3 dark:border-dark-700">
            <span class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ustcSpeed.samples.total', { count: formatNumber(sampleTotal) }) }}</span>
            <div class="flex items-center gap-2">
              <button type="button" class="btn btn-secondary !px-3 !py-1.5" :disabled="page <= 1 || samplesLoading" @click="changePage(page - 1)">{{ t('admin.ustcSpeed.action.previous') }}</button>
              <span class="min-w-20 text-center text-xs text-gray-500 dark:text-gray-400">{{ page }} / {{ pageCount }}</span>
              <button type="button" class="btn btn-secondary !px-3 !py-1.5" :disabled="page >= pageCount || samplesLoading" @click="changePage(page + 1)">{{ t('admin.ustcSpeed.action.next') }}</button>
            </div>
          </div>
        </section>
      </template>

      <template v-else>
        <section class="rounded-2xl border border-gray-200 bg-white p-5 shadow-sm dark:border-dark-700 dark:bg-dark-800">
          <div class="mb-4 flex flex-wrap items-start justify-between gap-3">
            <div>
              <h2 class="font-semibold text-gray-900 dark:text-white">{{ t('admin.ustcSpeed.settings.title') }}</h2>
              <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.ustcSpeed.settings.description') }}</p>
            </div>
            <button type="button" class="btn btn-secondary" :disabled="settingsLoading || settingsSaving" @click="loadSettings(true)">{{ t('admin.ustcSpeed.action.refresh') }}</button>
          </div>
          <div v-if="settingsLoading" class="py-8 text-center text-sm text-gray-500">{{ t('admin.ustcSpeed.action.loading') }}</div>
          <div v-else-if="!settingsLoaded" class="flex items-center justify-between gap-3 py-6 text-sm text-gray-500">
            <span>{{ t('admin.ustcSpeed.error.loadSettings') }}</span>
            <button type="button" class="btn btn-secondary" @click="loadSettings(true)">{{ t('admin.ustcSpeed.action.refresh') }}</button>
          </div>
          <form v-else class="space-y-4" @submit.prevent="saveGlobalSettings">
            <label class="flex items-start gap-2 text-sm text-gray-700 dark:text-gray-300">
              <input v-model="settings.capture_enabled" type="checkbox" class="mt-0.5 rounded border-gray-300 text-primary-600 focus:ring-primary-500" />
              <span><span class="block font-medium">{{ t('admin.ustcSpeed.settings.captureEnabled') }}</span><span class="mt-0.5 block text-xs text-gray-500">{{ t('admin.ustcSpeed.settings.disableCapture') }}</span></span>
            </label>
            <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
              <label class="filter-field"><span>{{ t('admin.ustcSpeed.settings.retentionDays') }}</span><input v-model.number="settings.retention_days" type="number" min="1" class="filter-input" /></label>
              <label class="filter-field"><span>{{ t('admin.ustcSpeed.settings.concurrency') }}</span><input v-model.number="settings.max_background_concurrency" type="number" min="1" class="filter-input" /></label>
              <label class="filter-field"><span>{{ t('admin.ustcSpeed.settings.requestBudget') }}</span><input v-model.number="settings.daily_request_budget" type="number" min="0" class="filter-input" /></label>
              <label class="filter-field"><span>{{ t('admin.ustcSpeed.settings.tokenBudget') }}</span><input v-model.number="settings.daily_output_token_budget" type="number" min="0" class="filter-input" /></label>
            </div>
            <div class="flex flex-wrap items-center justify-between gap-3">
              <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ustcSpeed.settings.perKey') }}</p>
              <button type="submit" class="btn btn-primary" :disabled="settingsSaving">{{ settingsSaving ? t('admin.ustcSpeed.action.saving') : t('admin.ustcSpeed.action.save') }}</button>
            </div>
            <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ustcSpeed.settings.budgetReset') }} {{ t('admin.ustcSpeed.settings.budgetCalibrations') }}</p>
          </form>
        </section>

        <section class="overflow-hidden rounded-2xl border border-gray-200 bg-white shadow-sm dark:border-dark-700 dark:bg-dark-800">
          <div class="flex flex-wrap items-center justify-between gap-3 border-b border-gray-100 px-5 py-4 dark:border-dark-700">
            <div><h2 class="font-semibold text-gray-900 dark:text-white">{{ t('admin.ustcSpeed.task.title') }}</h2><p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ustcSpeed.task.disabledByDefault') }}</p></div>
            <button type="button" class="btn btn-secondary" :disabled="tasksLoading" @click="loadTasks">{{ t('admin.ustcSpeed.action.refresh') }}</button>
          </div>
          <div v-if="tasksLoading" class="flex min-h-40 items-center justify-center text-sm text-gray-500">{{ t('admin.ustcSpeed.action.loading') }}</div>
          <div v-else-if="!tasks.length" class="px-5 py-12 text-center text-sm text-gray-500 dark:text-gray-400">{{ t('admin.ustcSpeed.task.empty') }}</div>
          <div v-else class="overflow-x-auto">
            <table class="min-w-[1080px] w-full divide-y divide-gray-100 text-left text-sm dark:divide-dark-700">
              <thead class="bg-gray-50 text-xs font-semibold uppercase tracking-wide text-gray-500 dark:bg-dark-900/40 dark:text-gray-400"><tr>
                <th class="px-4 py-3">{{ t('admin.ustcSpeed.task.field.name') }}</th><th class="px-4 py-3">{{ t('admin.ustcSpeed.task.selectModels') }}</th><th class="px-4 py-3">{{ t('admin.ustcSpeed.task.selectAccounts') }}</th><th class="px-4 py-3">{{ t('admin.ustcSpeed.task.nextRun') }}</th><th class="px-4 py-3">{{ t('admin.ustcSpeed.task.lastRun') }}</th><th class="px-4 py-3">{{ t('admin.ustcSpeed.task.lastStatus') }}</th><th class="px-4 py-3">{{ t('admin.ustcSpeed.samples.status') }}</th><th class="px-4 py-3">{{ t('admin.ustcSpeed.samples.action') }}</th>
              </tr></thead>
              <tbody class="divide-y divide-gray-100 dark:divide-dark-700"><tr v-for="task in tasks" :key="task.id" class="hover:bg-gray-50/70 dark:hover:bg-dark-700/30">
                <td class="px-4 py-3"><span class="block font-medium text-gray-900 dark:text-gray-100">{{ task.name }}</span><span class="text-xs text-gray-500">{{ t('admin.ustcSpeed.task.field.interval') }}: {{ task.interval_seconds }}</span></td>
                <td class="max-w-60 truncate px-4 py-3 text-gray-700 dark:text-gray-300">{{ task.models.join(', ') }}</td>
                <td class="px-4 py-3 text-gray-700 dark:text-gray-300">{{ task.all_accounts ? t('admin.ustcSpeed.task.allAccounts') : accountNames(task.account_ids) }}</td>
                <td class="whitespace-nowrap px-4 py-3 text-gray-600 dark:text-gray-300">{{ formatDateTime(task.next_run_at) }}</td>
                <td class="whitespace-nowrap px-4 py-3 text-gray-600 dark:text-gray-300">{{ formatDateTime(task.last_run_at) }}</td>
                <td class="px-4 py-3 text-gray-600 dark:text-gray-300">{{ task.last_status ? statusLabel(task.last_status) : '—' }}</td>
                <td class="px-4 py-3"><span :class="task.enabled ? 'status-pill status-success' : 'status-pill status-muted'">{{ task.enabled ? t('admin.ustcSpeed.task.enabled') : t('admin.ustcSpeed.status.paused') }}</span></td>
                <td class="px-4 py-3"><div class="flex items-center gap-2">
                  <button type="button" class="text-sm font-medium text-primary-600 hover:text-primary-700 dark:text-primary-300" @click="openTaskDialog(task)">{{ t('admin.ustcSpeed.action.edit') }}</button>
                  <button type="button" class="text-sm font-medium text-primary-600 hover:text-primary-700 dark:text-primary-300" @click="confirmRun(task)">{{ t('admin.ustcSpeed.task.manualRun') }}</button>
                  <button type="button" class="text-sm font-medium text-red-600 hover:text-red-700 dark:text-red-400" @click="confirmDelete(task)">{{ t('admin.ustcSpeed.action.delete') }}</button>
                </div></td>
              </tr></tbody>
            </table>
          </div>
        </section>

        <section class="overflow-hidden rounded-2xl border border-gray-200 bg-white shadow-sm dark:border-dark-700 dark:bg-dark-800">
          <div class="flex flex-wrap items-center justify-between gap-3 border-b border-gray-100 px-5 py-4 dark:border-dark-700">
            <div><h2 class="font-semibold text-gray-900 dark:text-white">{{ t('admin.ustcSpeed.runtime.title') }}</h2><p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ustcSpeed.runtime.description') }}</p></div>
            <button type="button" class="btn btn-secondary" :disabled="runtimeLoading" @click="loadRuntime">{{ t('admin.ustcSpeed.action.refresh') }}</button>
          </div>
          <div v-if="runtimeLoading" class="flex min-h-32 items-center justify-center text-sm text-gray-500">{{ t('admin.ustcSpeed.action.loading') }}</div>
          <div v-else-if="!runtime.length" class="px-5 py-8 text-center text-sm text-gray-500">{{ t('admin.ustcSpeed.runtime.noData') }}</div>
          <div v-else class="overflow-x-auto">
            <table class="min-w-[900px] w-full divide-y divide-gray-100 text-left text-sm dark:divide-dark-700">
              <thead class="bg-gray-50 text-xs font-semibold uppercase tracking-wide text-gray-500 dark:bg-dark-900/40 dark:text-gray-400"><tr>
                <th class="px-4 py-3">{{ t('admin.ustcSpeed.runtime.account') }}</th><th class="px-4 py-3">{{ t('admin.ustcSpeed.runtime.consecutive429') }}</th><th class="px-4 py-3">{{ t('admin.ustcSpeed.runtime.successStreak') }}</th><th class="px-4 py-3">{{ t('admin.ustcSpeed.runtime.notBefore') }}</th><th class="px-4 py-3">{{ t('admin.ustcSpeed.runtime.last429') }}</th><th class="px-4 py-3">{{ t('admin.ustcSpeed.runtime.leaseUntil') }}</th><th class="px-4 py-3">{{ t('admin.ustcSpeed.runtime.lastStatus') }}</th>
              </tr></thead>
              <tbody class="divide-y divide-gray-100 dark:divide-dark-700"><tr v-for="item in runtime" :key="item.account_id">
                <td class="px-4 py-3 font-medium text-gray-900 dark:text-gray-100">{{ item.account_name }}</td><td class="px-4 py-3 text-gray-700 dark:text-gray-300">{{ item.consecutive_429 }}</td><td class="px-4 py-3 text-gray-700 dark:text-gray-300">{{ item.success_streak }}</td><td class="px-4 py-3 text-gray-600 dark:text-gray-300">{{ formatDateTime(item.not_before) }}</td><td class="px-4 py-3 text-gray-600 dark:text-gray-300">{{ formatDateTime(item.last_429_at) }}</td><td class="px-4 py-3 text-gray-600 dark:text-gray-300">{{ formatDateTime(item.lease_until) }}</td><td class="px-4 py-3"><span :class="item.paused ? 'status-pill status-warning' : 'status-pill status-muted'">{{ item.paused ? t('admin.ustcSpeed.status.paused') : (item.last_status ? statusLabel(item.last_status) : '—') }}</span></td>
              </tr></tbody>
            </table>
          </div>
        </section>
      </template>

      <USTCSpeedTaskDialog
        :show="taskDialogOpen"
        :task="selectedTask"
        :accounts="dimensions.accounts"
        :models="modelOptions"
        :saving="taskSaving"
        @close="taskDialogOpen = false"
        @submit="saveTask"
      />
      <USTCSpeedSampleDialog :show="sampleDialogOpen" :sample="selectedSample" :loading="sampleDetailLoading" @close="sampleDialogOpen = false" />
      <ConfirmDialog
        :show="Boolean(pendingAction)"
        :title="confirmationTitle"
        :message="confirmationMessage"
        :confirm-text="confirmationButton"
        :danger="pendingAction?.kind === 'delete'"
        @cancel="pendingAction = null"
        @confirm="executePendingAction"
      />
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { Chart as ChartJS, Filler, Legend, LineElement, LinearScale, PointElement, Tooltip } from 'chart.js'
import { Line } from 'vue-chartjs'
import { computed, defineComponent, h, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import AppLayout from '@/components/layout/AppLayout.vue'
import DateRangePicker from '@/components/common/DateRangePicker.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import USTCSpeedTaskDialog from '@/components/admin/ustc-speed/USTCSpeedTaskDialog.vue'
import USTCSpeedSampleDialog from '@/components/admin/ustc-speed/USTCSpeedSampleDialog.vue'
import { adminUSTCSpeedAPI } from '@/api/admin/ustcSpeed'
import type {
  USTCSpeedAccountDimension,
  USTCSpeedAggregate,
  USTCSpeedMetric,
  USTCSpeedOverview,
  USTCSpeedPoint,
  USTCSpeedRuntime,
  USTCSpeedSample,
  USTCSpeedSettings,
  USTCSpeedSource,
  USTCSpeedTask,
  USTCSpeedTaskInput,
  USTCSpeedQuery,
} from '@/api/admin/ustcSpeed'
import { useAppStore } from '@/stores/app'

ChartJS.register(Filler, Legend, LineElement, LinearScale, PointElement, Tooltip)

type ChartGroup = 'tps' | 'ttft' | 'time'
type AccountView = 'accounts' | 'equal'
type ExportFormat = 'csv' | 'json'
type PendingAction = { kind: 'delete' | 'run'; task: USTCSpeedTask } | null
type SampleFilterState = {
  source: USTCSpeedSource
  model: string
  accountIds: number[]
  reasoning: 'all' | 'on' | 'off' | 'unknown'
  stream: 'all' | 'true' | 'false'
  status: string
  profile: string
  taskId: number | undefined
  inputMin: number | undefined
  inputMax: number | undefined
  outputMin: number | undefined
  outputMax: number | undefined
  interval: 'raw' | '5m' | '15m' | '1h' | '1d' | 'auto'
  aggregate: USTCSpeedAggregate
  timezone: string
}

const { t, locale } = useI18n()
const route = useRoute()
const router = useRouter()
const appStore = useAppStore()

const isOverview = computed(() => route.path === '/admin/ustc-speed')
const isSamples = computed(() => route.path === '/admin/ustc-speed/samples')
const isTasks = computed(() => route.path === '/admin/ustc-speed/tasks')
const pageTitle = computed(() => isSamples.value ? t('admin.ustcSpeed.samplesTitle') : isTasks.value ? t('admin.ustcSpeed.tasksTitle') : t('admin.ustcSpeed.title'))
const pageDescription = computed(() => isSamples.value ? t('admin.ustcSpeed.samplesDescription') : isTasks.value ? t('admin.ustcSpeed.tasksDescription') : t('admin.ustcSpeed.overviewDescription'))

const localDate = (date: Date): string => {
  const year = date.getFullYear()
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  return `${year}-${month}-${day}`
}
const today = localDate(new Date())
const weekAgoDate = new Date()
weekAgoDate.setDate(weekAgoDate.getDate() - 6)
const startDate = ref(localDate(weekAgoDate))
const endDate = ref(today)
const filters = reactive<SampleFilterState>({
  source: 'real',
  model: '',
  accountIds: [],
  reasoning: 'all',
  stream: 'all',
  status: '',
  profile: '',
  taskId: undefined,
  inputMin: undefined,
  inputMax: undefined,
  outputMin: undefined,
  outputMax: undefined,
  interval: 'auto',
  aggregate: 'p50',
  timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || 'Asia/Shanghai',
})

const dimensions = reactive<{ accounts: USTCSpeedAccountDimension[]; models: string[] }>({ accounts: [], models: [] })
const overview = ref<USTCSpeedOverview | null>(null)
const loading = ref(false)
const chartGroup = ref<ChartGroup>('tps')
const metric = ref<USTCSpeedMetric>('generation_tps')
const accountView = ref<AccountView>('accounts')
const samples = ref<USTCSpeedSample[]>([])
const sampleTotal = ref(0)
const samplesLoading = ref(false)
const page = ref(1)
const pageSize = ref(25)
const exporting = ref(false)
const selectedSample = ref<USTCSpeedSample | null>(null)
const sampleDialogOpen = ref(false)
const sampleDetailLoading = ref(false)

const tasks = ref<USTCSpeedTask[]>([])
const tasksLoading = ref(false)
const taskListLoaded = ref(false)
const taskDialogOpen = ref(false)
const selectedTask = ref<USTCSpeedTask | null>(null)
const taskSaving = ref(false)
const settings = ref<USTCSpeedSettings>({ capture_enabled: false, retention_days: 30, max_background_concurrency: 4, daily_request_budget: 100, daily_output_token_budget: 50000 })
const settingsLoading = ref(false)
const settingsSaving = ref(false)
const settingsLoaded = ref(false)
const runtime = ref<USTCSpeedRuntime[]>([])
const runtimeLoading = ref(false)
const pendingAction = ref<PendingAction>(null)

const statuses = ['success', 'error', 'rate_limited', 'skipped', 'cancelled', 'recorded']
const pageCount = computed(() => Math.max(1, Math.ceil(sampleTotal.value / pageSize.value)))
const accountFilterLabel = computed(() => filters.accountIds.length
  ? t('admin.ustcSpeed.filter.account') + ` · ${filters.accountIds.length}`
  : t('admin.ustcSpeed.filter.allAccounts'))
const modelOptions = computed(() => [...new Set([
  ...dimensions.models,
  ...dimensions.accounts.flatMap((account) => account.models ?? []),
  ...filteredPoints.value.map((point) => point.model),
  ...samples.value.map((sample) => sample.model),
].filter(Boolean))].sort((a, b) => a.localeCompare(b)))

const metricOptions = computed(() => {
  const options: Record<ChartGroup, Array<{ value: USTCSpeedMetric; label: string }>> = {
    tps: [
      { value: 'generation_tps', label: t('admin.ustcSpeed.metric.generationTps') },
      { value: 'overall_tps', label: t('admin.ustcSpeed.metric.overallTps') },
    ],
    ttft: [
      { value: 'ttft_ms', label: t('admin.ustcSpeed.metric.ttftMs') },
      { value: 'first_output_ms', label: t('admin.ustcSpeed.metric.firstOutputMs') },
    ],
    time: [
      { value: 'total_ms', label: t('admin.ustcSpeed.metric.totalMs') },
      { value: 'output_ms', label: t('admin.ustcSpeed.metric.outputMs') },
      { value: 'headers_ms', label: t('admin.ustcSpeed.metric.headersMs') },
      { value: 'tail_ms', label: t('admin.ustcSpeed.metric.tailMs') },
      { value: 'max_gap_ms', label: t('admin.ustcSpeed.metric.maxGapMs') },
    ],
  }
  return options[chartGroup.value]
})

const isTpsMetric = computed(() => metric.value === 'generation_tps' || metric.value === 'overall_tps')
const chartUnit = computed(() => isTpsMetric.value ? t('admin.ustcSpeed.metric.unitTps') : t('admin.ustcSpeed.metric.unitMs'))
const equalAverageNeedsAggregation = computed(() => {
  if (accountView.value !== 'equal' || (overview.value?.interval ?? filters.interval) !== 'raw') return false
  if (filters.source !== 'probe') return true
  return (overview.value?.series ?? []).some((point) => !point.round_id)
})

const filteredPoints = computed(() => overview.value?.series ?? [])
const validMetricPointCount = computed(() => filteredPoints.value.filter((point) => readValue(point) !== null).reduce((sum, point) => sum + Math.max(1, point.valid_count || 0), 0))
const hasLegacy = computed(() => filteredPoints.value.some((point) => point.profile.toLowerCase().startsWith('legacy')))

function readValue(point: USTCSpeedPoint): number | null {
  const aggregateValue = point[filters.aggregate]
  const value = point.value ?? (filters.interval === 'raw' ? null : aggregateValue)
  return value !== null && Number.isFinite(value) ? value : null
}

interface RenderPoint {
  time: string
  value: number | null
  sampleId?: number
  count: number
  validCount: number
  accountId?: number
  model: string
  source: string
  profile: string
  coverage?: { valid: number; total: number }
}

interface RenderSeries { key: string; label: string; model: string; source: string; profile: string; points: RenderPoint[] }

function eligibleAccountCount(model: string, source: string): number {
  const selected = filters.accountIds.length
    ? dimensions.accounts.filter((account) => filters.accountIds.includes(account.id))
    : dimensions.accounts
  const task = source === 'probe' && filters.taskId
    ? tasks.value.find((item) => item.id === filters.taskId)
    : undefined
  const taskAccountIds = task && !task.all_accounts ? new Set(task.account_ids) : undefined
  const capable = selected.filter((account) =>
    (!taskAccountIds || taskAccountIds.has(account.id)) &&
    (!account.models.length || account.models.includes(model)),
  )
  return Math.max(1, new Set(capable.map((account) => account.key_account_id || account.id)).size)
}

function physicalKeyId(accountId: number): number {
  return dimensions.accounts.find((account) => account.id === accountId)?.key_account_id || accountId
}

function makeRenderSeries(points: USTCSpeedPoint[]): RenderSeries[] {
  const grouped = new Map<string, { model: string; source: string; profile: string; accountName: string; accountId: number; points: USTCSpeedPoint[] }>()
  const profilesByScope = new Map<string, Set<string>>()
  for (const point of points) {
    const base = [point.model || '', point.source || '', point.profile || ''].join('|')
    const key = accountView.value === 'equal' ? base : `${base}|${point.account_id}`
    const scope = JSON.stringify([point.model || '', point.source || ''])
    const profiles = profilesByScope.get(scope) ?? new Set<string>()
    profiles.add(point.profile || '')
    profilesByScope.set(scope, profiles)
    const entry = grouped.get(key) ?? {
      model: point.model,
      source: point.source,
      profile: point.profile,
      accountName: point.account_name,
      accountId: point.account_id,
      points: [],
    }
    entry.points.push(point)
    grouped.set(key, entry)
  }

  const profileOrdinals = new Map<string, number>()
  for (const [scope, profiles] of profilesByScope) {
    const orderedProfiles = [...profiles].sort((a, b) => a.localeCompare(b))
    orderedProfiles.forEach((profile, index) => {
      profileOrdinals.set(`${scope}|${profile}`, index + 1)
    })
  }

  const out: RenderSeries[] = []
  for (const [key, group] of grouped) {
    const name = accountView.value === 'equal'
      ? t('admin.ustcSpeed.accountView.equal')
      : group.accountName || `${t('admin.ustcSpeed.overview.account')} #${group.accountId}`
    const scope = JSON.stringify([group.model || '', group.source || ''])
    const ordinal = profileOrdinals.get(`${scope}|${group.profile}`) ?? 1
    const profileLabel = !group.profile
      ? ''
      : group.profile.length <= 24 && !group.profile.includes(':')
        ? group.profile
        : t('admin.ustcSpeed.overview.configuration', { number: ordinal })
    const baseLabel = [group.model, profileLabel, name].filter(Boolean).join(' · ')
    const sourceName = group.source === 'probe' ? t('admin.ustcSpeed.source.probe') : t('admin.ustcSpeed.source.real')
    const bucketed = new Map<string, USTCSpeedPoint[]>()
    for (const point of group.points) {
      const bucket = accountView.value === 'equal' && group.source === 'probe' && point.round_id
        ? `round:${point.round_id}`
        : `time:${point.time}`
      const items = bucketed.get(bucket) ?? []
      items.push(point)
      bucketed.set(bucket, items)
    }

    const renderPoints: RenderPoint[] = []
    for (const entries of bucketed.values()) {
      const values = entries.map(readValue).filter((value): value is number => value !== null)
      let value: number | null = null
      let coverage: RenderPoint['coverage']
      if (accountView.value === 'equal') {
        const accountValues = new Map<number, number[]>()
        for (const point of entries) {
          const pointValue = readValue(point)
          if (pointValue === null) continue
          const keyAccountId = physicalKeyId(point.account_id)
          const valuesForAccount = accountValues.get(keyAccountId) ?? []
          valuesForAccount.push(pointValue)
          accountValues.set(keyAccountId, valuesForAccount)
        }
        const perAccount = [...accountValues.values()].map((accountPoints) => accountPoints.reduce((sum, current) => sum + current, 0) / accountPoints.length)
        value = perAccount.length ? perAccount.reduce((sum, current) => sum + current, 0) / perAccount.length : null
        coverage = { valid: perAccount.length, total: Math.max(perAccount.length, eligibleAccountCount(group.model, group.source)) }
      } else {
        value = values.length ? values[values.length - 1] : null
      }
      const timestamp = entries.reduce((earliest, point) => point.time < earliest ? point.time : earliest, entries[0].time)
      renderPoints.push({
        time: timestamp,
        value,
        sampleId: accountView.value === 'accounts' && entries.length === 1 ? entries[0].sample_id : undefined,
        count: entries.reduce((sum, point) => sum + (point.count || 0), 0),
        validCount: entries.reduce((sum, point) => sum + (point.valid_count || 0), 0),
        accountId: accountView.value === 'accounts' ? group.accountId : undefined,
        model: group.model,
        source: group.source,
        profile: group.profile,
        coverage,
      })
    }
    renderPoints.sort((a, b) => a.time.localeCompare(b.time))
    out.push({ key, label: `${baseLabel} · ${sourceName}`, model: group.model, source: group.source, profile: group.profile, points: renderPoints })
  }
  return out
}

const renderSeries = computed(() => makeRenderSeries(filteredPoints.value))
const chartData = computed<any>(() => {
  if (!renderSeries.value.length) return null
  if (!renderSeries.value.some((series) => series.points.length)) return null
  const palette = ['#4677d5', '#13a789', '#b461d1', '#e49727', '#4287a4', '#d35e77', '#7783d4', '#7c9b44']
  const datasets = renderSeries.value.map((series, index) => {
    return {
      label: series.label,
      data: series.points.map((point) => ({ x: new Date(point.time).getTime(), y: point.value })),
      borderColor: palette[index % palette.length],
      backgroundColor: palette[index % palette.length],
      borderDash: series.source === 'probe' ? [6, 4] : [],
      borderWidth: 2,
      pointRadius: accountView.value === 'equal' ? 2 : 2.5,
      pointHoverRadius: 5,
      pointHitRadius: 8,
      tension: 0.22,
      fill: false,
      spanGaps: false,
    }
  })
  return { datasets }
})

const chartOptions = computed<any>(() => {
  const dark = document.documentElement.classList.contains('dark')
  const text = dark ? '#a1a1aa' : '#6b7280'
  const grid = dark ? '#303741' : '#edf0f4'
  return {
    responsive: true,
    maintainAspectRatio: false,
    interaction: { mode: 'nearest' as const, intersect: false },
    plugins: {
      legend: { position: 'bottom' as const, labels: { color: text, usePointStyle: true, boxWidth: 7, padding: 16, font: { size: 11 } } },
      tooltip: {
        backgroundColor: dark ? '#171b20' : '#ffffff',
        titleColor: dark ? '#f3f4f6' : '#111827',
        bodyColor: dark ? '#d1d5db' : '#4b5563',
        borderColor: grid,
        borderWidth: 1,
        padding: 10,
        callbacks: {
          title: (items: Array<{ parsed: { x: number } }>) => {
            const timestamp = items[0]?.parsed?.x
            return Number.isFinite(timestamp) ? formatChartTooltipLabel(timestamp) : ''
          },
          label: (context: { dataset: { label?: string }; parsed: { y: number | null } }) => `${context.dataset.label ?? ''}: ${formatChartValue(context.parsed.y)}`,
          afterBody: (items: Array<{ datasetIndex: number; dataIndex: number }>) => {
            const item = items[0]
            if (!item) return []
            const point = renderSeries.value[item.datasetIndex]?.points[item.dataIndex]
            if (!point) return []
            const details: string[] = []
            if (point.profile) details.push(`${t('admin.ustcSpeed.samples.profile')}: ${point.profile}`)
            if (point.coverage) details.push(t('admin.ustcSpeed.overview.equalCoverage', point.coverage))
            else if (point.accountId) details.push(`${t('admin.ustcSpeed.overview.account')}: ${accountName(point.accountId)}`)
            return details
          },
        },
      },
    },
    scales: {
      x: {
        type: 'linear',
        grid: { display: false },
        ticks: {
          color: text,
          maxTicksLimit: 10,
          autoSkip: true,
          maxRotation: 0,
          callback: (value: string | number) => formatChartLabel(Number(value)),
        },
      },
      y: { grid: { color: grid, borderDash: [4, 4] }, ticks: { color: text, callback: (value: string | number) => Number(value).toLocaleString(locale.value === 'zh' ? 'zh-CN' : 'en-US') }, title: { display: true, text: chartUnit.value, color: text } },
    },
    onClick: (_event: unknown, active: Array<{ datasetIndex: number; index: number }>) => {
      const hit = active[0]
      if (!hit) return
      const point = renderSeries.value[hit.datasetIndex]?.points[hit.index]
      if (point) void openPoint(point)
    },
  }
})

const seriesBreakdown = computed(() => {
  const grouped = new Map<string, { key: string; model: string; source: string; profile: string; account: string; count: number; valid: number }>()
  for (const point of filteredPoints.value) {
    const key = [point.model, point.profile, point.source, point.account_id].join('|')
    const row = grouped.get(key) ?? {
      key,
      model: point.model,
      source: point.source,
      profile: point.profile,
      account: point.account_name || `#${point.account_id}`,
      count: 0,
      valid: 0,
    }
    row.count += point.count || 0
    row.valid += point.valid_count || 0
    grouped.set(key, row)
  }
  return [...grouped.values()].sort((a, b) => a.model.localeCompare(b.model) || a.account.localeCompare(b.account))
})

function formatChartLabel(value: number): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return ''
  const dateRange = new Date(endDate.value).getTime() - new Date(startDate.value).getTime()
  const options: Intl.DateTimeFormatOptions = dateRange > 86400000 * 1.2
    ? { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }
    : { hour: '2-digit', minute: '2-digit' }
  return formatChartDate(date, options)
}

function formatChartTooltipLabel(timestamp: number): string {
  return formatChartDate(new Date(timestamp), { year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

function formatChartDate(date: Date, options: Intl.DateTimeFormatOptions): string {
  const localeName = locale.value === 'zh' ? 'zh-CN' : 'en-US'
  const timeZone = filters.timezone || 'Asia/Shanghai'
  try {
    return new Intl.DateTimeFormat(localeName, { ...options, timeZone }).format(date)
  } catch {
    return new Intl.DateTimeFormat(localeName, options).format(date)
  }
}

function formatChartValue(value: number | null): string {
  if (value === null || !Number.isFinite(value)) return '—'
  return `${value.toLocaleString(locale.value === 'zh' ? 'zh-CN' : 'en-US', { maximumFractionDigits: 2 })} ${chartUnit.value}`
}

function buildQuery(): USTCSpeedQuery {
  return {
    start_date: startDate.value,
    end_date: endDate.value,
    timezone: filters.timezone || 'Asia/Shanghai',
    source: filters.source,
    model: filters.model || undefined,
    account_ids: filters.accountIds.length ? filters.accountIds.join(',') : undefined,
    task_id: numberFilter(filters.taskId),
    reasoning: filters.reasoning,
    stream: filters.stream,
    status: filters.status || undefined,
    profile: filters.profile || undefined,
    input_min: numberFilter(filters.inputMin),
    input_max: numberFilter(filters.inputMax),
    output_min: numberFilter(filters.outputMin),
    output_max: numberFilter(filters.outputMax),
    metric: metric.value,
    interval: filters.interval,
    aggregate: filters.aggregate,
  }
}

function numberFilter(value: number | undefined): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}

function toastError(message: string) { appStore.showToast('error', message) }

async function loadDimensions() {
  try {
    const result = await adminUSTCSpeedAPI.getDimensions()
    dimensions.accounts = (result.accounts ?? []).map((account) => ({ ...account, models: account.models ?? [] }))
    dimensions.models = result.models ?? []
  } catch {
    toastError(t('admin.ustcSpeed.error.loadDimensions'))
  }
}

async function loadOverview() {
  loading.value = true
  try {
    overview.value = await adminUSTCSpeedAPI.getOverview(buildQuery())
  } catch {
    overview.value = null
    toastError(t('admin.ustcSpeed.error.loadOverview'))
  } finally {
    loading.value = false
  }
}

async function loadSamples() {
  samplesLoading.value = true
  try {
    const result = await adminUSTCSpeedAPI.listSamples({ ...buildQuery(), limit: pageSize.value, offset: (page.value - 1) * pageSize.value })
    samples.value = result.items ?? []
    sampleTotal.value = result.total ?? 0
  } catch {
    samples.value = []
    sampleTotal.value = 0
    toastError(t('admin.ustcSpeed.error.loadSamples'))
  } finally {
    samplesLoading.value = false
  }
}

async function loadTasks() {
  tasksLoading.value = true
  try {
    const result = await adminUSTCSpeedAPI.listTasks()
    tasks.value = result.items ?? []
    taskListLoaded.value = true
  } catch {
    tasks.value = []
    toastError(t('admin.ustcSpeed.error.loadTasks'))
  } finally {
    tasksLoading.value = false
  }
}

async function loadSettings(force = false) {
  if (settingsLoaded.value && !force) return
  settingsLoading.value = true
  try {
    settings.value = await adminUSTCSpeedAPI.getSettings()
    settingsLoaded.value = true
  } catch {
    toastError(t('admin.ustcSpeed.error.loadSettings'))
  } finally {
    settingsLoading.value = false
  }
}

async function loadRuntime() {
  runtimeLoading.value = true
  try {
    const result = await adminUSTCSpeedAPI.getRuntime()
    runtime.value = result.items ?? []
  } catch {
    runtime.value = []
    toastError(t('admin.ustcSpeed.error.loadRuntime'))
  } finally {
    runtimeLoading.value = false
  }
}

async function loadCurrentSection() {
  if (isOverview.value) {
    const loadTaskAccounts = filters.taskId && filters.source !== 'real' && !taskListLoaded.value
      ? loadTasks()
      : Promise.resolve()
    await Promise.all([loadOverview(), loadTaskAccounts])
  }
  else if (isSamples.value) await loadSamples()
  else if (isTasks.value) await Promise.all([loadTasks(), loadSettings(), loadRuntime()])
}

function refreshCurrent() { void loadCurrentSection() }
function applyFilters() {
  page.value = 1
  void loadCurrentSection()
}
function useFiveMinuteAggregation() {
  filters.interval = '5m'
  applyFilters()
}
function changePage(value: number) {
  page.value = Math.min(pageCount.value, Math.max(1, value))
  void loadSamples()
}
function changePageSize() {
  page.value = 1
  void loadSamples()
}
function handleChartGroupChange() {
  metric.value = metricOptions.value[0].value
  void loadOverview()
}

function formatNumber(value: number | null | undefined): string {
  if (value === null || value === undefined || !Number.isFinite(value)) return '—'
  return new Intl.NumberFormat(locale.value === 'zh' ? 'zh-CN' : 'en-US').format(value)
}

function formatKnownTokenCount(value: number | null | undefined, known: boolean | undefined): string {
  return known === true ? formatNumber(value) : t('admin.ustcSpeed.detail.notObserved')
}

function formatDateTime(value: string | null | undefined): string {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '—' : new Intl.DateTimeFormat(locale.value === 'zh' ? 'zh-CN' : 'en-US', { dateStyle: 'medium', timeStyle: 'short' }).format(date)
}

function formatAge(value: string | null | undefined): string {
  if (!value) return t('admin.ustcSpeed.overview.ageUnknown')
  const timestamp = new Date(value).getTime()
  if (!Number.isFinite(timestamp)) return t('admin.ustcSpeed.overview.ageUnknown')
  const minutes = Math.max(0, Math.floor((Date.now() - timestamp) / 60000))
  if (minutes < 1) return t('admin.ustcSpeed.age.justNow')
  if (minutes < 60) return t('admin.ustcSpeed.age.minutes', { count: minutes })
  const hours = Math.floor(minutes / 60)
  if (hours < 48) return t('admin.ustcSpeed.age.hours', { count: hours })
  return t('admin.ustcSpeed.age.days', { count: Math.floor(hours / 24) })
}

function formatMilliseconds(value: number | null): string {
  return value === null || !Number.isFinite(value) ? '—' : `${value.toFixed(1)} ${t('admin.ustcSpeed.unit.milliseconds')}`
}

function formatRate(value: number | null): string {
  return value === null || !Number.isFinite(value) ? '—' : `${value.toFixed(2)} ${t('admin.ustcSpeed.unit.tokensPerSecond')}`
}

function sourceLabel(source: string): string {
  if (source === 'real') return t('admin.ustcSpeed.source.real')
  if (source === 'probe') return t('admin.ustcSpeed.source.probe')
  return source || '—'
}

function statusLabel(status: string): string {
  const supported = ['success', 'error', 'rate_limited', 'skipped', 'cancelled', 'recorded', 'queued', 'paused']
  return supported.includes(status) ? t(`admin.ustcSpeed.status.${status}`) : status || t('admin.ustcSpeed.status.unknown')
}

function sourceClass(source: string): string {
  return source === 'probe'
    ? 'status-pill status-probe'
    : 'status-pill status-real'
}

function statusClass(status: string): string {
  if (status === 'success') return 'status-pill status-success'
  if (status === 'rate_limited') return 'status-pill status-warning'
  if (status === 'recorded' || status === 'skipped' || status === 'cancelled') return 'status-pill status-muted'
  return 'status-pill status-error'
}

function accountName(id: number): string {
  return dimensions.accounts.find((account) => account.id === id)?.name || `#${id}`
}

function accountNames(ids: number[]): string {
  if (!ids?.length) return '—'
  return ids.map(accountName).join(', ')
}

function openTaskDialog(task: USTCSpeedTask | null = null) {
  selectedTask.value = task
  taskDialogOpen.value = true
}

async function saveTask(value: USTCSpeedTaskInput) {
  taskSaving.value = true
  try {
    if (value.id > 0) await adminUSTCSpeedAPI.updateTask(value.id, value)
    else await adminUSTCSpeedAPI.createTask(value)
    taskDialogOpen.value = false
    appStore.showToast('success', t('admin.ustcSpeed.task.saved'))
    await loadTasks()
  } catch {
    toastError(t('admin.ustcSpeed.error.saveTask'))
  } finally {
    taskSaving.value = false
  }
}

function confirmDelete(task: USTCSpeedTask) { pendingAction.value = { kind: 'delete', task } }
function confirmRun(task: USTCSpeedTask) { pendingAction.value = { kind: 'run', task } }

const confirmationTitle = computed(() => pendingAction.value?.kind === 'delete'
  ? t('admin.ustcSpeed.task.deleteTitle')
  : t('admin.ustcSpeed.task.runTitle'))
const confirmationMessage = computed(() => {
  if (!pendingAction.value) return ''
  return pendingAction.value.kind === 'delete'
    ? t('admin.ustcSpeed.task.deleteMessage', { name: pendingAction.value.task.name })
    : t('admin.ustcSpeed.task.runMessage', { name: pendingAction.value.task.name })
})
const confirmationButton = computed(() => pendingAction.value?.kind === 'delete'
  ? t('admin.ustcSpeed.action.delete')
  : t('admin.ustcSpeed.task.manualRun'))

async function executePendingAction() {
  const action = pendingAction.value
  if (!action) return
  pendingAction.value = null
  try {
    if (action.kind === 'delete') {
      await adminUSTCSpeedAPI.deleteTask(action.task.id)
      appStore.showToast('success', t('admin.ustcSpeed.task.deleted'))
      await loadTasks()
    } else {
      const result = await adminUSTCSpeedAPI.runTask(action.task.id)
      appStore.showToast('success', t('admin.ustcSpeed.task.runQueued', { round: result.round_id }))
      await Promise.all([loadTasks(), loadRuntime()])
    }
  } catch {
    toastError(action.kind === 'delete' ? t('admin.ustcSpeed.error.deleteTask') : t('admin.ustcSpeed.error.runTask'))
  }
}

async function saveGlobalSettings() {
  settingsSaving.value = true
  try {
    settings.value = await adminUSTCSpeedAPI.saveSettings({ ...settings.value })
    settingsLoaded.value = true
    appStore.showToast('success', t('admin.ustcSpeed.settings.saved'))
  } catch {
    toastError(t('admin.ustcSpeed.error.saveSettings'))
  } finally {
    settingsSaving.value = false
  }
}

async function openSample(id: number) {
  sampleDialogOpen.value = true
  sampleDetailLoading.value = true
  selectedSample.value = null
  try {
    selectedSample.value = await adminUSTCSpeedAPI.getSample(id)
  } catch {
    toastError(t('admin.ustcSpeed.error.loadSample'))
    sampleDialogOpen.value = false
  } finally {
    sampleDetailLoading.value = false
  }
}

async function openPoint(point: RenderPoint) {
  if (point.sampleId) {
    await openSample(point.sampleId)
    return
  }
  filters.model = point.model
  filters.source = point.source === 'probe' ? 'probe' : 'real'
  filters.profile = point.profile
  filters.accountIds = point.accountId ? [point.accountId] : []
  page.value = 1
  await router.push('/admin/ustc-speed/samples')
}

function escapeCsv(value: unknown): string {
  const text = value === null || value === undefined ? '' : String(value)
  return `"${text.replace(/"/g, '""')}"`
}

function csvFor(items: USTCSpeedSample[]): string {
  const columns: Array<[string, (item: USTCSpeedSample) => unknown]> = [
    [t('admin.ustcSpeed.samples.id'), (item) => item.id],
    [t('admin.ustcSpeed.samples.model'), (item) => item.model],
    [t('admin.ustcSpeed.samples.account'), (item) => item.account_name],
    [t('admin.ustcSpeed.samples.source'), (item) => sourceLabel(item.source)],
    [t('admin.ustcSpeed.samples.status'), (item) => statusLabel(item.status)],
    [t('admin.ustcSpeed.samples.profile'), (item) => item.profile],
    [t('admin.ustcSpeed.samples.startedAt'), (item) => item.started_at],
    [t('admin.ustcSpeed.samples.reasoning'), (item) => item.reasoning],
    [t('admin.ustcSpeed.samples.stream'), (item) => item.stream],
    [t('admin.ustcSpeed.samples.generationTps'), (item) => item.generation_tps],
    [t('admin.ustcSpeed.samples.ttft'), (item) => item.ttft_ms],
    [t('admin.ustcSpeed.samples.duration'), (item) => item.total_ms],
    [t('admin.ustcSpeed.samples.inputTokens'), (item) => item.input_tokens_known === true ? item.input_tokens : ''],
    [t('admin.ustcSpeed.samples.outputTokens'), (item) => item.token_count_known === true ? item.output_tokens : ''],
    [t('admin.ustcSpeed.detail.reasoningTokens'), (item) => item.reasoning_tokens],
  ]
  return [columns.map(([label]) => escapeCsv(label)).join(','), ...items.map((item) => columns.map(([, read]) => escapeCsv(read(item))).join(','))].join('\r\n')
}

function downloadFile(contents: string, fileName: string, mime: string) {
  const url = URL.createObjectURL(new Blob([contents], { type: mime }))
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = fileName
  anchor.click()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}

async function exportSamples(format: ExportFormat) {
  exporting.value = true
  try {
    const query = buildQuery()
    const limit = 500
    let offset = 0
    let total = Number.POSITIVE_INFINITY
    const items: USTCSpeedSample[] = []
    while (offset < total) {
      const result = await adminUSTCSpeedAPI.listSamples({ ...query, limit, offset })
      total = result.total ?? 0
      if (!result.items?.length) break
      items.push(...result.items)
      offset += result.items.length
      if (result.items.length < limit) break
    }
    if (!items.length) {
      appStore.showToast('info', t('admin.ustcSpeed.samples.noData'))
      return
    }
    const stamp = localDate(new Date())
    if (format === 'csv') downloadFile(`\uFEFF${csvFor(items)}`, `ustc-speed-samples-${stamp}.csv`, 'text/csv;charset=utf-8')
    else downloadFile(JSON.stringify({ exported_at: new Date().toISOString(), filters: query, total, samples: items }, null, 2), `ustc-speed-samples-${stamp}.json`, 'application/json;charset=utf-8')
  } catch {
    toastError(t('admin.ustcSpeed.error.exportSamples'))
  } finally {
    exporting.value = false
  }
}

watch(() => route.path, () => { void loadCurrentSection() })
watch(() => filters.aggregate, () => {
  if (isOverview.value && !loading.value) void loadOverview()
})
onMounted(async () => {
  await Promise.all([loadDimensions(), loadSettings()])
  await loadCurrentSection()
})

const SummaryValue = defineComponent({
  props: { label: { type: String, required: true }, value: { type: String, required: true } },
  setup(props) {
    return () => h('div', { class: 'min-w-0' }, [
      h('div', { class: 'text-xs text-gray-500 dark:text-gray-400' }, props.label),
      h('div', { class: 'mt-1 truncate font-semibold text-gray-900 dark:text-gray-100' }, props.value),
    ])
  },
})
</script>

<style scoped>
.filter-input {
  @apply w-full rounded-lg border border-gray-200 bg-white px-3 py-2 text-sm text-gray-800 outline-none transition;
  @apply focus:border-primary-500 focus:ring-2 focus:ring-primary-500/20;
  @apply dark:border-dark-600 dark:bg-dark-900 dark:text-gray-100;
}

.filter-field { @apply block space-y-1.5; }
.filter-field > span { @apply block text-xs font-medium text-gray-600 dark:text-gray-400; }

.form-input {
  @apply w-full rounded-lg border border-gray-300 bg-white px-3 py-2 text-sm text-gray-900 outline-none transition;
  @apply focus:border-primary-500 focus:ring-2 focus:ring-primary-500/20;
  @apply dark:border-dark-600 dark:bg-dark-900 dark:text-white;
}

.status-pill { @apply inline-flex whitespace-nowrap rounded-full px-2.5 py-1 text-xs font-medium; }
.status-success { @apply bg-emerald-50 text-emerald-700 dark:bg-emerald-900/25 dark:text-emerald-300; }
.status-warning { @apply bg-amber-50 text-amber-700 dark:bg-amber-900/25 dark:text-amber-300; }
.status-error { @apply bg-red-50 text-red-700 dark:bg-red-900/25 dark:text-red-300; }
.status-muted { @apply bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300; }
.status-probe { @apply bg-violet-50 text-violet-700 dark:bg-violet-900/25 dark:text-violet-300; }
.status-real { @apply bg-blue-50 text-blue-700 dark:bg-blue-900/25 dark:text-blue-300; }
</style>
