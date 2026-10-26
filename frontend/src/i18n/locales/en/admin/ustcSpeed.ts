export default {
  ustcSpeed: {
    title: 'USTC Speed Observatory',
    description: 'Observe upstream first-output latency, generation speed, and request stage timing from real calls and probes.',
    samplesTitle: 'Call Details',
    tasksTitle: 'Probe Tasks and Settings',
    overviewDescription: 'Real work calls are the default view. Probe data can be viewed alone or overlaid as separate series.',
    samplesDescription: 'Search captured calls and probe samples using the current filters.',
    tasksDescription: 'Manage scheduled probes, manual runs, global capture settings, and account cooldowns.',
    nav: { overview: 'Trends', tasks: 'Tasks and Settings' },
    action: {
      refresh: 'Refresh', apply: 'Apply filters', exportCsv: 'Export CSV', exportJson: 'Export JSON',
      addTask: 'New Task', edit: 'Edit', delete: 'Delete', run: 'Run Now', save: 'Save', cancel: 'Cancel',
      close: 'Close', confirm: 'Confirm', loading: 'Loading…', previous: 'Previous', next: 'Next',
      pageSize: 'Rows per page', showDetails: 'View details', reset: 'Reset', saving: 'Saving…', exporting: 'Exporting…'
    },
    source: { label: 'Data source', real: 'Real calls', probe: 'Probes', all: 'Real + probes (separate series)' },
    filter: {
      model: 'Model', allModels: 'All models', account: 'Account', allAccounts: 'All accounts',
      timeRange: 'Time range', advanced: 'Advanced filters', reasoning: 'Reasoning', all: 'All',
      reasoningOn: 'Reasoning on', reasoningOff: 'Reasoning off', reasoningUnknown: 'Unknown', stream: 'Streaming',
      streaming: 'Streaming', nonStreaming: 'Non-streaming', status: 'Result status', anyStatus: 'All statuses',
      profile: 'Profile', profilePlaceholder: 'Enter profile name', taskId: 'Task ID',
      taskIdHint: 'Task ID filters probe samples only. It is ignored for real calls and applies only to probes when both sources are selected.',
      inputMin: 'Minimum input tokens', inputMax: 'Maximum input tokens', outputMin: 'Minimum output tokens', outputMax: 'Maximum output tokens',
      timezone: 'Time zone', statusRecorded: 'Recorded (result unknown)'
    },
    metric: {
      view: 'Main chart', metric: 'Metric', tps: 'TPS trend', ttft: 'TTFT trend', time: 'Time stages',
      generationTps: 'Generation speed (excl. first-token wait)', overallTps: 'Average speed (incl. first-token wait)',
      ttftMs: 'Upstream time to first text', firstOutputMs: 'Upstream time to first output', totalMs: 'Actual task duration', outputMs: 'Actual output duration',
      headersMs: 'Upstream response headers', tailMs: 'Wait after the last output', maxGapMs: 'Longest pause during output', unitTps: 'tokens / second', unitMs: 'milliseconds'
    },
    interval: { label: 'Time interval', auto: 'Automatic', raw: 'Raw samples', m1: '1 minute', m5: '5 minutes', m15: '15 minutes', h1: '1 hour', d1: '1 day' },
    aggregate: { label: 'Statistic', p50: 'P50 median', mean: 'Mean', p10: 'P10 slow range', p90: 'P90' },
    overview: {
      chartTitle: 'Speed and wait trends',
      aggregation: '{interval} · {statistic}', bucketSamples: '{count} valid observations in this point',
      distribution: 'P10–P90 spread', spread: 'P10 / P50 / P90: {p10} / {p50} / {p90}',
      lowSamples: 'Fewer than 5 valid observations; treat this point as indicative only.',
      lastHours: 'Last {hours} hours',
      usageRecords: 'View usage records', probeDrilldown: 'Showing raw probe observations. Select a point to inspect its speed measurement.',
      noSeries: 'There are no observations to chart for these filters.', noSeriesHint: 'Empty periods are not filled with zeros or interpolated. Adjust the date range or filters.',
      samples: 'Samples', successful: 'Successful', rateLimited: 'Rate limited', realSamples: 'Real calls', probeSamples: 'Probes', outputTokens: 'Output tokens',
      failed: 'Errors', skipped: 'Skipped', cancelled: 'Cancelled',
      realLatest: 'Latest real call', probeLatest: 'Latest probe',
      realAge: 'Latest real call {age}', probeAge: 'Latest probe {age}',
      validPoints: 'Valid metric points',
      auxiliary: 'Coverage and series details', auxiliaryDescription: 'Review the latest data time, metric coverage, and sample counts by model series.', seriesBreakdown: 'Grouped by model and source',
      fallbackApplied: 'Probes are throttled or paused because of rate limits or capacity. Showing recent real calls from the last {minutes} minutes temporarily; the series remains labeled as real.',
      truncated: 'The backend truncated trend data to limit response size.', dropped: '{count} samples were omitted from the chart due to data limits.',
      noReal: 'No real call samples exist in this time range.', noProbe: 'No probe samples exist in this time range.',
      ageUnknown: 'Time unknown', profile: 'Profile', configuration: 'Config {number}', account: 'Account', source: 'Source', coverage: 'Valid account coverage'
    },
    hourly: {
      title: 'Calls by hour of day', description: 'Valid calls in the applied range, grouped by local hour. Use it to choose when to run long work.',
      samples: '{count} valid observations', noData: 'No valid calls in this range to compare by hour.',
      metric: 'Metric'
    },
    samples: {
      tableTitle: 'Calls and probe samples', total: '{count} total', noData: 'No samples match these filters.',
      startedAt: 'Started', model: 'Model', account: 'Account', source: 'Source', status: 'Status',
      generationTps: 'Generation TPS', ttft: 'Upstream first text', duration: 'Upstream request time', tokens: 'Input / output',
      profile: 'Profile', reasoning: 'Reasoning', stream: 'Stream', action: 'Action',
      loading: 'Loading samples…', exportLimit: 'More samples could not be loaded. The downloaded file contains the samples already fetched.',
      id: 'Sample ID', inputTokens: 'Input tokens', outputTokens: 'Output tokens'
    },
    status: {
      success: 'Success', error: 'Error', rate_limited: 'Rate limited', skipped: 'Skipped', cancelled: 'Cancelled',
      recorded: 'Recorded (result unknown)', queued: 'Queued', paused: 'Paused', unknown: 'Unknown',
      completed: 'Completed', failed: 'Failed', partial: 'Partially completed',
      no_accounts: 'No eligible accounts', outside_active_window: 'Outside active window'
    },
    detail: {
      title: 'Speed measurement', loading: 'Loading speed measurement…', overview: 'Measurement', metrics: 'Observed metrics',
      upstreamTimingNotice: 'New samples start timing when sent upstream and stop when the upstream response completes; Sub2API local queue time is excluded. First-text / first-output time is observed output availability, not exact model prefill time.',
      conditions: 'Call conditions', inputTokens: 'Input tokens', outputTokens: 'Output tokens', reasoningTokens: 'Reasoning tokens',
      cachedTokens: 'Cached tokens', events: 'Valid events', chars: 'Output characters', finishReason: 'Finish reason',
      observedModel: 'Model reported upstream', httpStatus: 'HTTP status', apiMode: 'API protocol', measurementMode: 'Measurement mode', inputTokenCountKnown: 'Input token count known', outputTokenCountKnown: 'Output token count known',
      reasoningChars: 'Reasoning characters', gapOver1s: 'Gaps over 1 second', gapOver2s: 'Gaps over 2 seconds', streamDone: 'Stream completed',
      yes: 'Yes', no: 'No', notObserved: 'Not observed', notAvailable: 'Not applicable', reason: 'Reason', task: 'Task ID',
      round: 'Round ID', startedAt: 'Started', finishedAt: 'Finished', errorCode: 'Error code',
      providerCost: 'Provider cost', legacyNotice: 'Real-call speed is derived from usage records: total duration, first-token time, and token counts. Event-level stage timings, output gaps, and a reliable result status are available only for probe samples.', measurementUsageLog: 'Usage record'
    },
    task: {
      title: 'Probe tasks', add: 'New probe task', edit: 'Edit probe task', deleteTitle: 'Delete probe task',
      deleteMessage: 'Delete “{name}”? Saved samples will remain.', runTitle: 'Run probe task',
      runMessage: 'Queue “{name}” for a background run. The server will still apply user priority, capacity, and 429 protections.',
      runQueued: 'Probe task queued (round {round}).', saved: 'Probe task saved.', deleted: 'Probe task deleted.',
      enabled: 'Enable scheduled runs', disabledByDefault: 'New tasks are disabled by default and will not make upstream calls automatically.',
      empty: 'No probe tasks yet. New tasks are disabled by default, so you can review the configuration before enabling them.', noAccounts: 'No accounts are available for probing.',
      noModels: 'There are no models in the directory yet. Enter a model name below.', selectAccounts: 'Select accounts', selectModels: 'Select models', allAccounts: 'Include all available accounts automatically',
      modelPlaceholder: 'Enter a model name', addModel: 'Add model', removeModel: 'Remove model {model}',
      manualRun: 'Run now', nextRun: 'Next run', lastRun: 'Last run', lastStatus: 'Last result',
      target: 'Targets', schedule: 'Schedule and time window', request: 'Request settings', safety: 'Capacity and rate limits',
      intervalUnit: { seconds: 'seconds', minutes: 'minutes', hours: 'hours' }, everyInterval: 'Every {count} {unit}', editSchedule: 'Change interval',
      fallback: 'Real data fallback', advanced: 'Advanced settings', advancedSchedule: 'Advanced scheduling',
      field: {
        name: 'Name', interval: 'Probe interval', jitter: 'Round jitter (seconds)', timezone: 'Time zone',
        cronExpression: 'Cron expression (5 fields)',
        activeStart: 'Active from', activeEnd: 'Active until', days: 'Days of week', apiMode: 'API protocol', stream: 'Streaming output',
        prompt: 'Synthetic prompt', promptHint: 'Probes use synthetic prompts. Real call prompts and responses are not stored.', promptVariants: 'Prompt variants (one per line)',
        maxTokens: 'Output limit', temperature: 'Temperature', topP: 'Top P', seed: 'Seed', stop: 'Stop sequences (one per line)',
        thinkingMode: 'Reasoning mode', reasoningEffort: 'Reasoning effort', timeout: 'Request timeout (seconds)', firstOutputTimeout: 'First output timeout (seconds)',
        maxParallel: 'Maximum accounts per round', maxStartSkew: 'Allowed start skew (seconds)', reserveRpm: 'Reserve RPM',
        reserveConcurrency: 'Reserve concurrency', maxActive: 'Maximum active requests', skipWaiting: 'Skip probes while users are waiting',
        backoffAfter429: '429s before backing off', backoffWindow: '429 observation window (seconds)', backoffMultiplier: 'Backoff multiplier',
        maxInterval: 'Maximum probe interval (seconds)', pauseAfter429: 'Consecutive 429s before pausing', recoverySuccesses: 'Successes required to recover',
        realFallback: 'Use real calls while probes are paused', realFallbackAge: 'Maximum age of real data (seconds)', realFallbackSamples: 'Minimum real samples',
      },
      hint: {
        perKeyConcurrency: 'Background concurrency is fixed at one request per physical key. Global concurrency is also bounded by this task and system settings.',
        capacities: 'Account capabilities come from the dimensions API. The server checks model access and shared capacity before sending requests.',
        physicalKeyGroup: 'Physical key group #{id}',
        days: 'Leave all days selected to run every day.',
        cronExpression: 'Leave empty to use the interval. Cron is interpreted in this task time zone; fields are minute, hour, day, month, weekday.',
        cronOverridesInterval: 'Cron schedule takes precedence over the interval when set.',
        realFallback: 'Real calls keep their own source and conditions; they are not added to probe success rates.',
        zeroUnlimited: 'An active-request limit of 0 adds no task-specific limit. RPM reserve may be 0; at least one concurrency slot must be reserved.'
      },
      validation: { name: 'Enter a task name.', models: 'Select at least one model.', accounts: 'Select accounts or enable automatic account inclusion.', numbers: 'Enter valid numeric settings.' },
      defaultPrompt: 'Briefly explain a scientific concept you know well.',
      apis: { chat: 'Chat Completions', responses: 'Responses' },
      thinking: { off: 'Off', on: 'On', auto: 'Automatic (no extra setting)' },
      effort: { none: 'Unset', low: 'Low', medium: 'Medium', high: 'High', xhigh: 'Extra high' },
      reason: {
        userLoad: 'User load or capacity', backgroundCapacity: 'Background concurrency full', sharedAdmission: 'Shared admission blocked',
        paused429: '429 pause', backoff429: '429 backoff', limitsUnknown: 'Quota limits unknown', modelNotSupported: 'Model not supported by account',
        roundStartSkew: 'Start skew exceeded', runtimeUnavailable: 'Runtime state unavailable', gatewayUnavailable: 'Gateway unavailable',
        invalidRequest: 'Invalid request', invalidEndpoint: 'Invalid endpoint', localCapacity: 'Local capacity', noAccounts: 'No eligible accounts',
        outsideActiveWindow: 'Outside active window', accountsUnavailable: 'Account list unavailable', stopped: 'Stopped', cancelled: 'Cancelled', taskChanged: 'Task edited',
        upstreamNetworkError: 'Upstream network error', upstreamHttpError: 'Upstream HTTP error', upstreamStreamError: 'Upstream stream error',
        streamIncomplete: 'Stream incomplete', emptyOutput: 'Empty output', firstOutputTimeout: 'First-output timeout', observerUnavailable: 'Observer unavailable',
        upstream429: 'Upstream 429', requestCancelled: 'Request cancelled', timeout: 'Timeout', upstreamIoError: 'Upstream I/O error'
      },
      weekday: { sun: 'Sun', mon: 'Mon', tue: 'Tue', wed: 'Wed', thu: 'Thu', fri: 'Fri', sat: 'Sat' }
    },
    settings: {
      title: 'Real-call sync and background concurrency', description: 'Real-call speed is computed from existing usage records and synced in the background. Set probe frequency in each task; shared RPM, concurrency, and 429 protections still apply.',
      captureEnabled: 'Sync real calls from usage records', concurrency: 'Maximum background probe concurrency',
      perKey: 'Background concurrency is limited to one request per physical key.', saved: 'Global settings saved.',
      disableCapture: 'When off, no new usage records are imported for real-call speed. Probe tasks are unaffected.'
    },
    runtime: {
      title: 'Account runtime status', description: 'Shared 429 cooldowns, pauses, and background leases.',
      account: 'Account', consecutive429: 'Consecutive 429s', successStreak: 'Success streak', notBefore: 'Cooldown until',
      last429: 'Last 429', paused: 'Paused', leaseUntil: 'Background lease until', lastStatus: 'Last status',
      noData: 'No account runtime status is available.'
    },
    error: { loadDimensions: 'Could not load account and model dimensions.', loadOverview: 'Could not load the speed trend.', loadSamples: 'Could not load call samples.',
      loadTasks: 'Could not load probe tasks.', loadSettings: 'Could not load global settings.', loadRuntime: 'Could not load account runtime status.',
      saveTask: 'Could not save the probe task.', deleteTask: 'Could not delete the probe task.', runTask: 'Could not queue the probe task.',
      saveSettings: 'Could not save global settings.', loadSample: 'Could not load call details.', exportSamples: 'Could not export samples.' },
    unit: { milliseconds: 'ms', seconds: 'seconds', tokensPerSecond: 'tokens / second', tokens: 'tokens', requests: 'requests', days: 'days' },
    age: { justNow: 'just now', minutes: '{count} min ago', hours: '{count} hr ago', days: '{count} days ago' }
  }
}
