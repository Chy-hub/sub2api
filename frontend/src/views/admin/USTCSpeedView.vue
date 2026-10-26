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
          <template v-if="isOverview">
            <button type="button" class="btn btn-secondary" :disabled="loading" @click="refreshCurrent">
              {{ loading ? t('admin.ustcSpeed.action.loading') : t('admin.ustcSpeed.action.refresh') }}
            </button>
            <button v-if="filters.source !== 'probe' || overview?.fallback_applied" type="button" class="btn btn-secondary" @click="openUsageRecords()">
              {{ t('admin.ustcSpeed.overview.usageRecords') }}
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
            <DateRangePicker ref="datePickerRef" v-model:start-date="startDate" v-model:end-date="endDate" @change="applyDateRange" />
          </div>
          <button type="button" class="btn btn-primary h-[42px]" :disabled="loading" @click="applyAllFilters">
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

      <template v-if="isOverview">
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
              <span v-if="overview" class="mt-2 inline-flex rounded-md bg-primary-50 px-2 py-1 text-xs font-medium text-primary-700 dark:bg-primary-900/20 dark:text-primary-300">{{ aggregationLabel }}</span>
              <p v-if="appliedQuery" class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ appliedRangeLabel }}</p>
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
                <select v-model="metric" class="filter-input" data-testid="chart-metric" @change="loadOverview">
                  <option v-for="option in metricOptions" :key="option.value" :value="option.value">{{ option.label }}</option>
                </select>
              </label>
              <label class="filter-field min-w-32">
                <span>{{ t('admin.ustcSpeed.interval.label') }}</span>
                <select v-model="filters.interval" class="filter-input" @change="loadOverview">
                  <option value="auto">{{ t('admin.ustcSpeed.interval.auto') }}</option>
                  <option value="1m">{{ t('admin.ustcSpeed.interval.m1') }}</option>
                  <option value="5m">{{ t('admin.ustcSpeed.interval.m5') }}</option>
                  <option value="15m">{{ t('admin.ustcSpeed.interval.m15') }}</option>
                  <option value="1h">{{ t('admin.ustcSpeed.interval.h1') }}</option>
                  <option value="1d">{{ t('admin.ustcSpeed.interval.d1') }}</option>
                  <option value="raw">{{ t('admin.ustcSpeed.interval.raw') }}</option>
                </select>
              </label>
            </div>
          </div>
          <div class="px-3 py-3 sm:px-5 sm:py-4">
            <div v-if="loading" class="flex h-[420px] items-center justify-center text-sm text-gray-500">{{ t('admin.ustcSpeed.action.loading') }}</div>
            <div v-else-if="!chartData || !chartData.datasets.length" class="flex min-h-[340px] flex-col items-center justify-center px-6 text-center">
              <div class="mb-2 flex h-12 w-12 items-center justify-center rounded-full bg-gray-100 text-xl text-gray-400 dark:bg-dark-700">∅</div>
              <p class="font-medium text-gray-700 dark:text-gray-200">{{ t('admin.ustcSpeed.overview.noSeries') }}</p>
              <p class="mt-1 max-w-lg text-sm text-gray-500 dark:text-gray-400">{{ t('admin.ustcSpeed.overview.noSeriesHint') }}</p>
            </div>
            <div v-else class="h-[420px] min-w-0">
              <SpeedChart type="bar" :data="chartData" :options="chartOptions" />
            </div>
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

        <section v-if="overview" class="overflow-hidden rounded-2xl border border-gray-200 bg-white shadow-sm dark:border-dark-700 dark:bg-dark-800">
          <div class="flex flex-wrap items-end justify-between gap-3 border-b border-gray-100 px-5 py-4 dark:border-dark-700">
            <div>
              <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.ustcSpeed.hourly.title') }}</h2>
              <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ustcSpeed.hourly.description') }}</p>
              <p v-if="appliedQuery" class="mt-1 text-xs text-gray-400 dark:text-gray-500">{{ appliedRangeLabel }}</p>
            </div>
            <label class="filter-field min-w-40">
              <span>{{ t('admin.ustcSpeed.hourly.metric') }}</span>
              <select v-model="hourlyMetric" class="filter-input" data-testid="hourly-metric" @change="loadOverview">
                <option value="generation_tps">{{ t('admin.ustcSpeed.hourly.metricGeneration') }}</option>
                <option value="overall_tps">{{ t('admin.ustcSpeed.hourly.metricOverall') }}</option>
              </select>
            </label>
          </div>
          <div class="px-3 py-3 sm:px-5 sm:py-4">
            <div v-if="!hourlySeries.length" class="flex h-[220px] items-center justify-center px-6 text-center text-sm text-gray-500 dark:text-gray-400">{{ t('admin.ustcSpeed.hourly.noData') }}</div>
            <div v-else class="h-[260px] min-w-0">
              <SpeedChart type="line" :data="hourlyChartData" :options="hourlyChartOptions" />
            </div>
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
                    <tr><th class="px-3 py-2">{{ t('admin.ustcSpeed.samples.model') }}</th><th class="px-3 py-2">{{ t('admin.ustcSpeed.overview.source') }}</th><th class="px-3 py-2">n</th><th class="px-3 py-2">{{ t('admin.ustcSpeed.samples.status') }}</th></tr>
                  </thead>
                  <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
                    <tr v-for="series in seriesBreakdown" :key="series.key" class="text-gray-700 dark:text-gray-300">
                      <td class="px-3 py-2">{{ series.model }}</td><td class="px-3 py-2">{{ sourceLabel(series.source) }}</td><td class="px-3 py-2">{{ formatNumber(series.count) }}</td><td class="px-3 py-2">{{ formatNumber(series.valid) }} / {{ formatNumber(series.count) }}</td>
                    </tr>
                  </tbody>
                </table>
              </div>
            </div>
          </div>
        </details>
      </template>

      <template v-else>
        <section class="overflow-hidden rounded-2xl border border-gray-200 bg-white shadow-sm dark:border-dark-700 dark:bg-dark-800">
          <div class="flex flex-wrap items-center justify-between gap-3 border-b border-gray-100 px-5 py-4 dark:border-dark-700">
            <div><h2 class="font-semibold text-gray-900 dark:text-white">{{ t('admin.ustcSpeed.task.title') }}</h2><p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ustcSpeed.task.disabledByDefault') }}</p></div>
            <button type="button" class="btn btn-secondary" :disabled="tasksLoading" @click="loadTasks()">{{ t('admin.ustcSpeed.action.refresh') }}</button>
          </div>
          <div v-if="tasksLoading" class="flex min-h-40 items-center justify-center text-sm text-gray-500">{{ t('admin.ustcSpeed.action.loading') }}</div>
          <div v-else-if="!tasks.length" class="px-5 py-12 text-center text-sm text-gray-500 dark:text-gray-400">{{ t('admin.ustcSpeed.task.empty') }}</div>
          <div v-else class="overflow-x-auto">
            <table class="min-w-[1080px] w-full divide-y divide-gray-100 text-left text-sm dark:divide-dark-700">
              <thead class="bg-gray-50 text-xs font-semibold uppercase tracking-wide text-gray-500 dark:bg-dark-900/40 dark:text-gray-400"><tr>
                <th class="px-4 py-3">{{ t('admin.ustcSpeed.task.field.name') }}</th><th class="px-4 py-3">{{ t('admin.ustcSpeed.task.selectModels') }}</th><th class="px-4 py-3">{{ t('admin.ustcSpeed.task.selectAccounts') }}</th><th class="px-4 py-3">{{ t('admin.ustcSpeed.task.nextRun') }}</th><th class="px-4 py-3">{{ t('admin.ustcSpeed.task.lastRun') }}</th><th class="px-4 py-3">{{ t('admin.ustcSpeed.task.lastStatus') }}</th><th class="px-4 py-3">{{ t('admin.ustcSpeed.samples.status') }}</th><th class="px-4 py-3">{{ t('admin.ustcSpeed.samples.action') }}</th>
              </tr></thead>
              <tbody class="divide-y divide-gray-100 dark:divide-dark-700"><tr v-for="task in tasks" :key="task.id" class="hover:bg-gray-50/70 dark:hover:bg-dark-700/30">
                <td class="px-4 py-3"><span class="block font-medium text-gray-900 dark:text-gray-100">{{ task.name }}</span><span class="mt-1 inline-flex rounded-md bg-primary-50 px-2 py-1 text-xs font-medium text-primary-700 dark:bg-primary-900/20 dark:text-primary-300">{{ taskScheduleLabel(task) }}</span><button type="button" class="ml-2 text-xs font-medium text-primary-600 hover:underline dark:text-primary-300" @click="openTaskDialog(task)">{{ t('admin.ustcSpeed.task.editSchedule') }}</button></td>
                <td class="max-w-60 truncate px-4 py-3 text-gray-700 dark:text-gray-300">{{ task.models.join(', ') }}</td>
                <td class="px-4 py-3 text-gray-700 dark:text-gray-300">{{ task.all_accounts ? t('admin.ustcSpeed.task.allAccounts') : accountNames(task.account_ids) }}</td>
                <td class="whitespace-nowrap px-4 py-3 text-gray-600 dark:text-gray-300">{{ formatDateTime(task.next_run_at) }}</td>
                <td class="whitespace-nowrap px-4 py-3 text-gray-600 dark:text-gray-300">{{ formatDateTime(task.last_run_at) }}</td>
                <td class="px-4 py-3 text-gray-600 dark:text-gray-300">
                  <span>{{ task.last_status ? statusLabel(task.last_status) : '—' }}</span>
                  <span v-if="task.last_detail" class="mt-0.5 block text-xs text-gray-400 dark:text-gray-500">{{ taskDetailLabel(task.last_detail) }}</span>
                </td>
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

        <details class="rounded-2xl border border-gray-200 bg-white shadow-sm dark:border-dark-700 dark:bg-dark-800">
          <summary class="cursor-pointer px-5 py-4 text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.ustcSpeed.settings.title') }}</summary>
          <div class="border-t border-gray-100 p-5 dark:border-dark-700">
            <p class="mb-4 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.ustcSpeed.settings.description') }}</p>
            <div v-if="settingsLoading" class="py-4 text-sm text-gray-500">{{ t('admin.ustcSpeed.action.loading') }}</div>
            <div v-else-if="!settingsLoaded" class="flex items-center justify-between gap-3 text-sm text-gray-500">
              <span>{{ t('admin.ustcSpeed.error.loadSettings') }}</span>
              <button type="button" class="btn btn-secondary" @click="loadSettings(true)">{{ t('admin.ustcSpeed.action.refresh') }}</button>
            </div>
            <form v-else class="space-y-4" @submit.prevent="saveGlobalSettings">
              <label class="flex items-start gap-2 text-sm text-gray-700 dark:text-gray-300">
                <input v-model="settings.capture_enabled" type="checkbox" class="mt-0.5 rounded border-gray-300 text-primary-600 focus:ring-primary-500" />
                <span><span class="block font-medium">{{ t('admin.ustcSpeed.settings.captureEnabled') }}</span><span class="mt-0.5 block text-xs text-gray-500">{{ t('admin.ustcSpeed.settings.disableCapture') }}</span></span>
              </label>
              <label class="filter-field max-w-xs"><span>{{ t('admin.ustcSpeed.settings.concurrency') }}</span><input v-model.number="settings.max_background_concurrency" type="number" min="1" max="16" class="filter-input" /></label>
              <div class="flex flex-wrap items-center justify-between gap-3">
                <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ustcSpeed.settings.perKey') }}</p>
                <button type="submit" class="btn btn-primary" :disabled="settingsSaving">{{ settingsSaving ? t('admin.ustcSpeed.action.saving') : t('admin.ustcSpeed.action.save') }}</button>
              </div>
            </form>
          </div>
        </details>

        <section class="overflow-hidden rounded-2xl border border-gray-200 bg-white shadow-sm dark:border-dark-700 dark:bg-dark-800">
          <div class="flex flex-wrap items-center justify-between gap-3 border-b border-gray-100 px-5 py-4 dark:border-dark-700">
            <div><h2 class="font-semibold text-gray-900 dark:text-white">{{ t('admin.ustcSpeed.runtime.title') }}</h2><p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ustcSpeed.runtime.description') }}</p></div>
            <button type="button" class="btn btn-secondary" :disabled="runtimeLoading" @click="loadRuntime()">{{ t('admin.ustcSpeed.action.refresh') }}</button>
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
import { Chart as ChartJS, BarController, BarElement, Legend, LineController, LineElement, LinearScale, PointElement, ScatterController, Tooltip } from 'chart.js'
import { Chart as SpeedChart } from 'vue-chartjs'
import { computed, defineComponent, h, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
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
  USTCSpeedHourPoint,
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

ChartJS.register(BarController, BarElement, Legend, LineController, LineElement, LinearScale, PointElement, ScatterController, Tooltip)

type ChartGroup = 'tps' | 'ttft' | 'time'
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
  interval: 'raw' | '1m' | '5m' | '15m' | '1h' | '1d' | 'auto'
  aggregate: USTCSpeedAggregate
  timezone: string
}

const { t, locale } = useI18n()
const route = useRoute()
const router = useRouter()
const appStore = useAppStore()

const isOverview = computed(() => route.path === '/admin/ustc-speed')
const isTasks = computed(() => route.path === '/admin/ustc-speed/tasks')
const pageTitle = computed(() => isTasks.value ? t('admin.ustcSpeed.tasksTitle') : t('admin.ustcSpeed.title'))
const pageDescription = computed(() => isTasks.value ? t('admin.ustcSpeed.tasksDescription') : t('admin.ustcSpeed.overviewDescription'))

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
// The shared picker exposes only calendar dates. Its "last 24 hours" preset is
// translated into an exact rolling window instead of two whole days.
const lastHours = ref<number | null>(null)
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
  aggregate: 'mean',
  timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || 'Asia/Shanghai',
})

const dimensions = reactive<{ accounts: USTCSpeedAccountDimension[]; models: string[] }>({ accounts: [], models: [] })
const overview = ref<USTCSpeedOverview | null>(null)
const appliedQuery = ref<USTCSpeedQuery | null>(null)
const datePickerRef = ref<InstanceType<typeof DateRangePicker> | null>(null)
const loading = ref(false)
let overviewRequestSeq = 0
let overviewAbortController: AbortController | null = null
let overviewViewDisposed = false
const chartGroup = ref<ChartGroup>('tps')
const metric = ref<USTCSpeedMetric>('generation_tps')
// The hourly view answers a different question ("when is the upstream fastest?")
// than the trend chart, so it keeps its own TPS metric instead of inheriting
// latency/diagnostic metrics that real usage records cannot provide.
const hourlyMetric = ref<'generation_tps' | 'overall_tps'>('generation_tps')
const exporting = ref(false)
const selectedSample = ref<USTCSpeedSample | null>(null)
const sampleDialogOpen = ref(false)
const sampleDetailLoading = ref(false)

const tasks = ref<USTCSpeedTask[]>([])
const tasksLoading = ref(false)
const taskDialogOpen = ref(false)
const selectedTask = ref<USTCSpeedTask | null>(null)
const taskSaving = ref(false)
const settings = ref<USTCSpeedSettings>({ capture_enabled: false, max_background_concurrency: 4 })
const settingsLoading = ref(false)
const settingsSaving = ref(false)
const settingsLoaded = ref(false)
const runtime = ref<USTCSpeedRuntime[]>([])
const runtimeLoading = ref(false)
const pendingAction = ref<PendingAction>(null)
let tasksRefreshTimer: number | undefined

function startTasksAutoRefresh() {
  stopTasksAutoRefresh()
  tasksRefreshTimer = window.setInterval(() => {
    if (!isTasks.value) return
    void Promise.all([loadTasks(true), loadRuntime(true)])
  }, 10000)
}

function stopTasksAutoRefresh() {
  if (tasksRefreshTimer !== undefined) {
    window.clearInterval(tasksRefreshTimer)
    tasksRefreshTimer = undefined
  }
}

const statuses = ['success', 'error', 'rate_limited', 'skipped', 'cancelled', 'recorded']
const accountFilterLabel = computed(() => filters.accountIds.length
  ? t('admin.ustcSpeed.filter.account') + ` · ${filters.accountIds.length}`
  : t('admin.ustcSpeed.filter.allAccounts'))
const modelOptions = computed(() => [...new Set([
  ...dimensions.models,
  ...dimensions.accounts.flatMap((account) => account.models ?? []),
  ...filteredPoints.value.map((point) => point.model),
].filter(Boolean))].sort((a, b) => a.localeCompare(b)))

const metricOptions = computed(() => {
  // Real calls come from usage records: they only have one first-token
  // timestamp and no response-header/stream-gap timing. Showing probe-only
  // metrics in the real view produced empty or duplicate charts.
  const probeMetrics = filters.source !== 'real'
  const options: Record<ChartGroup, Array<{ value: USTCSpeedMetric; label: string }>> = {
    tps: [
      { value: 'generation_tps', label: t('admin.ustcSpeed.metric.generationTps') },
      { value: 'overall_tps', label: t('admin.ustcSpeed.metric.overallTps') },
    ],
    ttft: [
      { value: 'ttft_ms', label: t('admin.ustcSpeed.metric.ttftMs') },
      ...(probeMetrics ? [{ value: 'first_output_ms' as const, label: t('admin.ustcSpeed.metric.firstOutputMs') }] : []),
    ],
    time: [
      { value: 'total_ms', label: t('admin.ustcSpeed.metric.totalMs') },
      { value: 'output_ms', label: t('admin.ustcSpeed.metric.outputMs') },
      ...(probeMetrics ? [
        { value: 'headers_ms' as const, label: t('admin.ustcSpeed.metric.headersMs') },
        { value: 'tail_ms' as const, label: t('admin.ustcSpeed.metric.tailMs') },
        { value: 'max_gap_ms' as const, label: t('admin.ustcSpeed.metric.maxGapMs') },
      ] : []),
    ],
  }
  return options[chartGroup.value]
})

const isTpsMetric = computed(() => (overview.value?.metric ?? metric.value).endsWith('tps'))
const chartUnit = computed(() => isTpsMetric.value ? t('admin.ustcSpeed.metric.unitTps') : t('admin.ustcSpeed.metric.unitMs'))
const hourlyUnit = computed(() => t('admin.ustcSpeed.metric.unitTps'))

function normalizeMetric() {
  if (!metricOptions.value.some((option) => option.value === metric.value)) {
    metric.value = metricOptions.value[0].value
  }
}
const aggregationLabel = computed(() => {
  const interval = overview.value?.interval ?? filters.interval
  const intervalKeys = { auto: 'auto', raw: 'raw', '1m': 'm1', '5m': 'm5', '15m': 'm15', '1h': 'h1', '1d': 'd1' }
  if (interval === 'raw') return t('admin.ustcSpeed.interval.raw')
  return t('admin.ustcSpeed.overview.aggregation', {
    interval: t(`admin.ustcSpeed.interval.${intervalKeys[interval]}`),
    statistic: t(`admin.ustcSpeed.aggregate.${overview.value?.aggregate || filters.aggregate}`),
  })
})
const filteredPoints = computed(() => overview.value?.series ?? [])
const validMetricPointCount = computed(() => filteredPoints.value.filter((point) => readValue(point) !== null).reduce((sum, point) => sum + Math.max(1, point.valid_count || 0), 0))

function formatRangeTimestamp(timestamp: number): string {
  return formatChartDate(new Date(timestamp), { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
}

const appliedRangeLabel = computed(() => {
  const query = appliedQuery.value
  if (!query) return ''
  if (query.last_hours) {
    const start = overview.value?.range_start ? Date.parse(overview.value.range_start) : NaN
    const end = overview.value?.range_end ? Date.parse(overview.value.range_end) : NaN
    const window = Number.isFinite(start) && Number.isFinite(end)
      ? `${formatRangeTimestamp(start)} — ${formatRangeTimestamp(end)}`
      : ''
    return [t('admin.ustcSpeed.overview.lastHours', { hours: query.last_hours }), window, query.timezone].filter(Boolean).join(' · ')
  }
  return `${query.start_date} — ${query.end_date} · ${query.timezone}`
})

function readValue(point: USTCSpeedPoint): number | null {
  const aggregateValue = point[overview.value?.aggregate ?? filters.aggregate]
  const value = point.value ?? (overview.value?.interval === 'raw' ? null : aggregateValue)
  return value !== null && Number.isFinite(value) ? value : null
}

interface RenderPoint {
  time: string
  value: number | null
  p10: number | null
  p50: number | null
  p90: number | null
  sampleId?: number
  count: number
  validCount: number
  accountId?: number
  model: string
  source: string
  profile: string
}

interface RenderSeries { key: string; label: string; model: string; source: string; profile: string; points: RenderPoint[] }

function makeRenderSeries(points: USTCSpeedPoint[]): RenderSeries[] {
  const grouped = new Map<string, RenderSeries>()
  for (const point of points) {
    const key = JSON.stringify([point.model, point.source, point.profile])
    const series = grouped.get(key) ?? {
      key,
      model: point.model,
      source: point.source,
      profile: point.profile,
      label: '',
      points: [],
    }
    series.points.push({
      time: point.time,
      value: readValue(point),
      p10: point.p10,
      p50: point.p50,
      p90: point.p90,
      sampleId: point.sample_id,
      count: point.count,
      validCount: point.valid_count,
      accountId: point.account_id > 0 ? point.account_id : undefined,
      model: point.model,
      source: point.source,
      profile: point.profile,
    })
    grouped.set(key, series)
  }
  const sources = sourceCountsByModel(points)
  for (const series of grouped.values()) {
    const showSource = (sources.get(series.model)?.size ?? 0) > 1
    series.label = seriesLabel(series.model, showSource ? sourceLabel(series.source) : '')
    series.points.sort((a, b) => a.time.localeCompare(b.time))
  }
  return [...grouped.values()]
}

const renderSeries = computed(() => makeRenderSeries(filteredPoints.value))
const chartTimeBounds = computed(() => {
  const query = appliedQuery.value
  const start = Date.parse(overview.value?.range_start || (query ? `${query.start_date}T00:00:00` : ''))
  const nextDay = query ? new Date(`${query.end_date}T00:00:00`) : new Date(NaN)
  nextDay.setDate(nextDay.getDate() + 1)
  const end = overview.value?.range_end ? Date.parse(overview.value.range_end) : nextDay.getTime()
  return Number.isFinite(start) && Number.isFinite(end) && end > start ? { start, end } : null
})
const bucketDurations: Record<string, number> = { '1m': 60000, '5m': 300000, '15m': 900000, '1h': 3600000, '1d': 86400000 }
const bucketMilliseconds = computed(() => bucketDurations[overview.value?.interval ?? ''] || 0)
function plottedTime(point: RenderPoint): number {
  const time = Date.parse(point.time)
  if (!bucketMilliseconds.value) return time
  const start = Math.max(time, chartTimeBounds.value?.start ?? time)
  const end = Math.min(time + bucketMilliseconds.value, chartTimeBounds.value?.end ?? Infinity)
  return start + Math.max(0, end - start) / 2
}
const chartPalette = ['#3b82f6', '#10b981', '#f59e0b', '#8b5cf6', '#06b6d4', '#ef4444', '#6366f1', '#14b8a6']

function tint(hex: string, alpha: string): string {
  return `${hex}${alpha}`
}

function seriesLabel(model: string, source = ''): string {
  return [model, source].filter(Boolean).join(' · ')
}

function sourceCountsByModel(points: Array<{ model: string; source: string }>): Map<string, Set<string>> {
  const out = new Map<string, Set<string>>()
  for (const point of points) {
    const sources = out.get(point.model) ?? new Set<string>()
    sources.add(point.source)
    out.set(point.model, sources)
  }
  return out
}

const chartTheme = computed(() => {
  const dark = document.documentElement.classList.contains('dark')
  return {
    dark,
    text: dark ? '#9ca3af' : '#6b7280',
    grid: dark ? '#374151' : '#f3f4f6',
    tooltipBg: dark ? '#171b20' : '#ffffff',
    tooltipTitle: dark ? '#f3f4f6' : '#111827',
    tooltipBody: dark ? '#d1d5db' : '#4b5563',
  }
})

const chartData = computed<any>(() => {
  if (!renderSeries.value.some((series) => series.points.length)) return null
  const raw = overview.value?.interval === 'raw'
  const realSeriesCount = renderSeries.value.filter((series) => series.source === 'real').length
  const datasets: any[] = []
  renderSeries.value.forEach((series, index) => {
    const color = chartPalette[index % chartPalette.length]
    const bar = !raw && series.source === 'real'
    // The P10-P90 spread sits behind the average bar. It is shown only for a
    // single real series so overlapping bands cannot mislead comparisons.
    if (bar && realSeriesCount === 1) {
      const ranges = series.points
        .filter((point) => Number.isFinite(point.p10) && Number.isFinite(point.p90))
        .map((point) => ({ x: plottedTime(point), y: [point.p10, point.p90] }))
      if (ranges.length) {
        datasets.push({
          type: 'bar',
          label: t('admin.ustcSpeed.overview.distribution'),
          data: ranges,
          backgroundColor: tint(color, '26'),
          borderColor: tint(color, '4d'),
          borderWidth: 1,
          borderRadius: 3,
          grouped: false,
          order: 2,
          maxBarThickness: 18,
          categoryPercentage: 1,
          barPercentage: 1,
          distribution: true,
          seriesIndex: index,
        })
      }
    }
    datasets.push({
      type: raw ? 'scatter' : bar ? 'bar' : 'line',
      label: series.label,
      data: series.points.map((point) => ({ x: plottedTime(point), y: point.value })),
      borderColor: color,
      backgroundColor: bar ? tint(color, 'cc') : color,
      borderDash: series.source === 'probe' ? [6, 4] : [],
      borderWidth: bar ? 0 : 2,
      borderRadius: bar ? 3 : 0,
      maxBarThickness: 14,
      categoryPercentage: 0.85,
      barPercentage: 0.8,
      pointRadius: raw ? 3 : 2.5,
      pointHoverRadius: 5,
      pointHitRadius: 8,
      showLine: !raw && !bar,
      tension: series.source === 'probe' ? 0.18 : 0,
      fill: false,
      spanGaps: false,
      order: 1,
      seriesIndex: index,
    })
  })
  return { datasets }
})

function pointAt(datasetIndex: number, dataIndex: number): RenderPoint | undefined {
  const dataset = chartData.value?.datasets?.[datasetIndex]
  if (!dataset || dataset.distribution) return undefined
  return renderSeries.value[dataset.seriesIndex]?.points[dataIndex]
}

const chartOptions = computed<any>(() => {
  const theme = chartTheme.value
  return {
    responsive: true,
    maintainAspectRatio: false,
    interaction: { mode: 'nearest' as const, intersect: false },
    plugins: {
      legend: {
        position: 'bottom' as const,
        labels: {
          color: theme.text,
          usePointStyle: true,
          pointStyle: 'circle' as const,
          boxWidth: 7,
          padding: 16,
          font: { size: 11 },
          filter: (item: { datasetIndex: number }, data: { datasets: any[] }) => !data.datasets[item.datasetIndex]?.distribution,
        },
      },
      tooltip: {
        backgroundColor: theme.tooltipBg,
        titleColor: theme.tooltipTitle,
        bodyColor: theme.tooltipBody,
        borderColor: theme.grid,
        borderWidth: 1,
        padding: 10,
        filter: (item: { dataset: any }) => !item.dataset.distribution,
        callbacks: {
          title: (items: Array<{ parsed: { x: number }; datasetIndex: number; dataIndex: number }>) => {
            const item = items[0]
            const point = item && pointAt(item.datasetIndex, item.dataIndex)
            if (!point) return ''
            const timestamp = Date.parse(point.time)
            if (!bucketMilliseconds.value) return formatChartTooltipLabel(timestamp)
            const start = Math.max(timestamp, chartTimeBounds.value?.start ?? timestamp)
            const end = Math.min(timestamp + bucketMilliseconds.value, chartTimeBounds.value?.end ?? Infinity)
            return `${formatChartTooltipLabel(start)} — ${formatChartTooltipLabel(end)}`
          },
          label: (context: { dataset: { label?: string }; parsed: { y: number | null } }) => `${context.dataset.label ?? ''}: ${formatChartValue(context.parsed.y)}`,
          afterBody: (items: Array<{ datasetIndex: number; dataIndex: number }>) => {
            const item = items[0]
            if (!item) return []
            const point = pointAt(item.datasetIndex, item.dataIndex)
            if (!point) return []
            const details: string[] = []
            details.push(t('admin.ustcSpeed.overview.bucketSamples', { count: point.validCount }))
            if (bucketMilliseconds.value > 0 && Number.isFinite(point.p10) && Number.isFinite(point.p50) && Number.isFinite(point.p90)) {
              details.push(t('admin.ustcSpeed.overview.spread', {
                p10: formatChartValue(point.p10),
                p50: formatChartValue(point.p50),
                p90: formatChartValue(point.p90),
              }))
            }
            if (bucketMilliseconds.value > 0 && point.validCount < 5) details.push(t('admin.ustcSpeed.overview.lowSamples'))
            if (point.accountId) details.push(`${t('admin.ustcSpeed.overview.account')}: ${accountName(point.accountId)}`)
            return details
          },
        },
      },
    },
    scales: {
      x: {
        type: 'linear',
        min: chartTimeBounds.value?.start,
        max: chartTimeBounds.value?.end,
        offset: false,
        grid: { display: false },
        ticks: {
          color: theme.text,
          maxTicksLimit: 10,
          autoSkip: true,
          maxRotation: 0,
          font: { size: 10 },
          callback: (value: string | number) => formatChartLabel(Number(value)),
        },
      },
      y: {
        beginAtZero: true,
        grid: { color: theme.grid, borderDash: [4, 4] },
        ticks: { color: theme.text, font: { size: 10 }, callback: (value: string | number) => Number(value).toLocaleString(locale.value === 'zh' ? 'zh-CN' : 'en-US') },
        title: { display: true, text: chartUnit.value, color: theme.text },
      },
    },
    onClick: (_event: unknown, active: Array<{ datasetIndex: number; index: number }>) => {
      const hit = active.find((item) => !chartData.value?.datasets?.[item.datasetIndex]?.distribution)
      if (!hit) return
      const point = pointAt(hit.datasetIndex, hit.index)
      if (point) void openPoint(point)
    },
  }
})

interface HourlySeries { key: string; label: string; source: string; hours: Array<USTCSpeedHourPoint | undefined> }

const hourlySeries = computed<HourlySeries[]>(() => {
  const points = overview.value?.hourly ?? []
  const sources = sourceCountsByModel(points)
  const grouped = new Map<string, HourlySeries>()
  for (const point of points) {
    if (point.hour < 0 || point.hour > 23) continue
    const key = JSON.stringify([point.model, point.source, point.profile])
    const series = grouped.get(key) ?? {
      key,
      label: seriesLabel(point.model, (sources.get(point.model)?.size ?? 0) > 1 ? sourceLabel(point.source) : ''),
      source: point.source,
      hours: Array.from({ length: 24 }, () => undefined),
    }
    series.hours[point.hour] = point
    grouped.set(key, series)
  }
  return [...grouped.values()]
})

function hourlyValue(point: USTCSpeedHourPoint | undefined): number | null {
  if (!point) return null
  const aggregate = overview.value?.aggregate ?? filters.aggregate
  const value = point[aggregate] ?? point.value
  return value !== null && Number.isFinite(value) ? value : null
}

const hourlyChartData = computed<any>(() => {
  if (!hourlySeries.value.length) return null
  return {
    labels: Array.from({ length: 24 }, (_, hour) => `${String(hour).padStart(2, '0')}:00`),
    datasets: hourlySeries.value.map((series, index) => {
      const color = chartPalette[index % chartPalette.length]
      return {
        label: series.label,
        data: series.hours.map(hourlyValue),
        borderColor: color,
        backgroundColor: tint(color, '33'),
        borderDash: series.source === 'probe' ? [6, 4] : [],
        borderWidth: 2,
        pointRadius: 2.5,
        pointHoverRadius: 5,
        pointHitRadius: 8,
        tension: 0.2,
        fill: false,
        spanGaps: false,
      }
    }),
  }
})

const hourlyChartOptions = computed<any>(() => {
  const theme = chartTheme.value
  return {
    responsive: true,
    maintainAspectRatio: false,
    interaction: { mode: 'index' as const, intersect: false },
    plugins: {
      legend: { position: 'bottom' as const, labels: { color: theme.text, usePointStyle: true, pointStyle: 'circle' as const, boxWidth: 7, padding: 16, font: { size: 11 } } },
      tooltip: {
        backgroundColor: theme.tooltipBg,
        titleColor: theme.tooltipTitle,
        bodyColor: theme.tooltipBody,
        borderColor: theme.grid,
        borderWidth: 1,
        padding: 10,
        callbacks: {
          title: (items: Array<{ dataIndex: number }>) => {
            const hour = items[0]?.dataIndex
            return hour === undefined ? '' : `${String(hour).padStart(2, '0')}:00–${String((hour + 1) % 24).padStart(2, '0')}:00`
          },
          label: (context: { dataset: { label?: string }; parsed: { y: number | null } }) => `${context.dataset.label ?? ''}: ${formatHourlyValue(context.parsed.y)}`,
          afterLabel: (context: { datasetIndex: number; dataIndex: number }) => {
            const point = hourlySeries.value[context.datasetIndex]?.hours[context.dataIndex]
            if (!point) return ''
            return [
              t('admin.ustcSpeed.hourly.samples', { count: point.valid_count }),
              t('admin.ustcSpeed.hourly.days', { count: point.day_count }),
            ]
          },
        },
      },
    },
    scales: {
      x: {
        grid: { display: false },
        ticks: { color: theme.text, maxTicksLimit: 12, autoSkip: true, maxRotation: 0, font: { size: 10 } },
      },
      y: {
        beginAtZero: true,
        grid: { color: theme.grid, borderDash: [4, 4] },
        ticks: { color: theme.text, font: { size: 10 }, callback: (value: string | number) => Number(value).toLocaleString(locale.value === 'zh' ? 'zh-CN' : 'en-US') },
        title: { display: true, text: hourlyUnit.value, color: theme.text },
      },
    },
  }
})

const seriesBreakdown = computed(() => {
  const grouped = new Map<string, { key: string; model: string; source: string; profile: string; count: number; valid: number }>()
  for (const point of filteredPoints.value) {
    const key = JSON.stringify([point.model, point.profile, point.source])
    const row = grouped.get(key) ?? {
      key,
      model: point.model,
      source: point.source,
      profile: point.profile,
      count: 0,
      valid: 0,
    }
    row.count += point.count || 0
    row.valid += point.valid_count || 0
    grouped.set(key, row)
  }
  return [...grouped.values()].sort((a, b) => a.model.localeCompare(b.model) || a.source.localeCompare(b.source))
})

function formatChartLabel(value: number): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return ''
  const dateRange = chartTimeBounds.value ? chartTimeBounds.value.end - chartTimeBounds.value.start : 0
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
  const timeZone = appliedQuery.value?.timezone || filters.timezone || 'Asia/Shanghai'
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

function formatHourlyValue(value: number | null): string {
  if (value === null || !Number.isFinite(value)) return '—'
  return `${value.toLocaleString(locale.value === 'zh' ? 'zh-CN' : 'en-US', { maximumFractionDigits: 2 })} ${hourlyUnit.value}`
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
    group_by: 'model',
    hourly: true,
    hourly_metric: hourlyMetric.value,
    last_hours: lastHours.value ?? undefined,
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
  if (overviewViewDisposed) return
  const requestSeq = ++overviewRequestSeq
  overviewAbortController?.abort()
  const controller = new AbortController()
  overviewAbortController = controller
  loading.value = true
  try {
    normalizeMetric()
    const query = buildQuery()
    const result = await adminUSTCSpeedAPI.getOverview(query, { signal: controller.signal })
    if (requestSeq !== overviewRequestSeq) return
    overview.value = result
    appliedQuery.value = query
  } catch {
    if (requestSeq !== overviewRequestSeq || controller.signal.aborted) return
    overview.value = null
    toastError(t('admin.ustcSpeed.error.loadOverview'))
  } finally {
    if (requestSeq === overviewRequestSeq) {
      overviewAbortController = null
      loading.value = false
    }
  }
}

async function loadTasks(silent = false) {
  if (!silent) tasksLoading.value = true
  try {
    const result = await adminUSTCSpeedAPI.listTasks()
    tasks.value = result.items ?? []
  } catch {
    if (!silent) {
      tasks.value = []
      toastError(t('admin.ustcSpeed.error.loadTasks'))
    }
  } finally {
    if (!silent) tasksLoading.value = false
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

async function loadRuntime(silent = false) {
  if (!silent) runtimeLoading.value = true
  try {
    const result = await adminUSTCSpeedAPI.getRuntime()
    runtime.value = result.items ?? []
  } catch {
    if (!silent) {
      runtime.value = []
      toastError(t('admin.ustcSpeed.error.loadRuntime'))
    }
  } finally {
    if (!silent) runtimeLoading.value = false
  }
}

async function loadCurrentSection() {
  if (isOverview.value) {
    stopTasksAutoRefresh()
    await loadOverview()
  } else if (isTasks.value) {
    startTasksAutoRefresh()
    await Promise.all([loadTasks(), loadSettings(), loadRuntime()])
  } else {
    stopTasksAutoRefresh()
  }
}

function refreshCurrent() { void loadCurrentSection() }
function applyFilters() {
  void loadCurrentSection()
}
// The filter bar's Apply button must commit a pending date selection too;
// otherwise the picker's local dates are discarded and the page reloads the
// previous range.
function applyAllFilters() {
  if (datePickerRef.value) datePickerRef.value.apply()
  else applyFilters()
}
function applyDateRange(range: { startDate: string; endDate: string; preset?: string | null }) {
  startDate.value = range.startDate
  endDate.value = range.endDate
  lastHours.value = range.preset === 'last24Hours' ? 24 : null
  applyFilters()
}
function handleChartGroupChange() {
  metric.value = metricOptions.value[0].value
  void loadOverview()
}

function formatNumber(value: number | null | undefined): string {
  if (value === null || value === undefined || !Number.isFinite(value)) return '—'
  return new Intl.NumberFormat(locale.value === 'zh' ? 'zh-CN' : 'en-US').format(value)
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

function sourceLabel(source: string): string {
  if (source === 'real') return t('admin.ustcSpeed.source.real')
  if (source === 'probe') return t('admin.ustcSpeed.source.probe')
  return source || '—'
}

function statusLabel(status: string): string {
  const supported = ['success', 'error', 'rate_limited', 'skipped', 'cancelled', 'recorded', 'queued', 'paused', 'completed', 'failed', 'partial', 'no_accounts', 'outside_active_window']
  return supported.includes(status) ? t(`admin.ustcSpeed.status.${status}`) : status || t('admin.ustcSpeed.status.unknown')
}

const taskReasonLabels: Record<string, string> = {
  user_load_or_capacity: 'userLoad',
  background_capacity: 'backgroundCapacity',
  shared_admission: 'sharedAdmission',
  '429_paused': 'paused429',
  '429_backoff': 'backoff429',
  limits_unknown: 'limitsUnknown',
  model_not_supported: 'modelNotSupported',
  round_start_skew: 'roundStartSkew',
  runtime_unavailable: 'runtimeUnavailable',
  gateway_unavailable: 'gatewayUnavailable',
  invalid_request: 'invalidRequest',
  invalid_endpoint: 'invalidEndpoint',
  local_capacity: 'localCapacity',
  no_accounts: 'noAccounts',
  outside_active_window: 'outsideActiveWindow',
  accounts_unavailable: 'accountsUnavailable',
  stopped: 'stopped',
  cancelled: 'cancelled',
  task_changed: 'taskChanged',
  upstream_network_error: 'upstreamNetworkError',
  upstream_http_error: 'upstreamHttpError',
  upstream_stream_error: 'upstreamStreamError',
  stream_incomplete: 'streamIncomplete',
  empty_output: 'emptyOutput',
  first_output_timeout: 'firstOutputTimeout',
  observer_unavailable: 'observerUnavailable',
  upstream_429: 'upstream429',
  request_cancelled: 'requestCancelled',
  timeout: 'timeout',
  upstream_io_error: 'upstreamIoError',
}

// Last-run detail is stored as "reason×count, reason×count".
function taskDetailLabel(detail: string): string {
  return detail.split(',').map((part) => {
    const [reason, count] = part.trim().split('×')
    const key = taskReasonLabels[reason]
    const label = key ? t(`admin.ustcSpeed.task.reason.${key}`) : reason
    return count ? `${label} ×${count}` : label
  }).join(' · ')
}

function accountName(id: number): string {
  return dimensions.accounts.find((account) => account.id === id)?.name || `#${id}`
}

function accountNames(ids: number[]): string {
  if (!ids?.length) return '—'
  return ids.map(accountName).join(', ')
}

function taskScheduleLabel(task: USTCSpeedTask): string {
  if (task.cron_expression) return `Cron · ${task.cron_expression}`
  const seconds = task.interval_seconds
  const unit = seconds % 3600 === 0 ? 'hours' : seconds % 60 === 0 ? 'minutes' : 'seconds'
  const value = unit === 'hours' ? seconds / 3600 : unit === 'minutes' ? seconds / 60 : seconds
  return t('admin.ustcSpeed.task.everyInterval', { count: value, unit: t(`admin.ustcSpeed.task.intervalUnit.${unit}`) })
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

function appliedRange(): { start: string; end: string } {
  const applied = appliedQuery.value
  return applied
    ? { start: applied.start_date, end: applied.end_date }
    : { start: startDate.value, end: endDate.value }
}

function openUsageRecords(point?: RenderPoint) {
  const range = appliedRange()
  const appliedAccountIds = appliedQuery.value?.account_ids?.split(',').map((value) => Number(value)).filter((value) => Number.isFinite(value) && value > 0) ?? []
  void router.push({
    path: '/admin/usage',
    query: {
      start_date: range.start,
      end_date: range.end,
      model: point?.model || appliedQuery.value?.model || undefined,
      account_id: point?.accountId || (appliedAccountIds.length === 1 ? appliedAccountIds[0] : undefined),
    },
  })
}

async function openPoint(point: RenderPoint) {
  if (point.sampleId) {
    await openSample(point.sampleId)
    return
  }
  if (point.source === 'real') {
    openUsageRecords(point)
    return
  }
  // Probe measurements remain inside the trend page; real requests have a
  // single record page under Usage.
  filters.model = point.model
  filters.source = 'probe'
  if (point.accountId) filters.accountIds = [point.accountId]
  filters.interval = 'raw'
  await loadOverview()
  appStore.showToast('info', t('admin.ustcSpeed.overview.probeDrilldown'))
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
    const query = appliedQuery.value ? { ...appliedQuery.value } : buildQuery()
    const limit = 200
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
  if (isOverview.value) void loadOverview()
})
onMounted(async () => {
  await Promise.all([loadDimensions(), loadSettings()])
  if (overviewViewDisposed) return
  await loadCurrentSection()
})
onUnmounted(() => {
  stopTasksAutoRefresh()
  overviewViewDisposed = true
  overviewRequestSeq += 1
  overviewAbortController?.abort()
  overviewAbortController = null
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
