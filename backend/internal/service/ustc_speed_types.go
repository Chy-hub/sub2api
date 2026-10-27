package service

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
)

// USTCSpeedObservation contains measurements only; no prompt/response text or credentials.
// Nullable times and rates mean not observed or not comparable, never zero speed.
type USTCSpeedObservation struct {
	HeadersMS            *float64 `json:"headers_ms"`
	TotalMS              *float64 `json:"total_ms"`
	FirstOutputMS        *float64 `json:"first_output_ms"`
	TTFTMS               *float64 `json:"ttft_ms"`
	OutputMS             *float64 `json:"output_ms"`
	TailMS               *float64 `json:"tail_ms"`
	GenerationTPS        *float64 `json:"generation_tps"`
	OverallTPS           *float64 `json:"overall_tps"`
	MaxGapMS             *float64 `json:"max_gap_ms"`
	InputTokens          int      `json:"input_tokens"`
	OutputTokens         int      `json:"output_tokens"`
	ReasoningTokens      int      `json:"reasoning_tokens"`
	CachedTokens         int      `json:"cached_tokens"`
	InputTokensKnown     bool     `json:"input_tokens_known"`
	TokenCountKnown      bool     `json:"token_count_known"`
	ReasoningTokensKnown bool     `json:"reasoning_tokens_known"`
	CachedTokensKnown    bool     `json:"cached_tokens_known"`
	OutputChars          int      `json:"output_chars"`
	ReasoningChars       int      `json:"reasoning_chars"`
	EventCount           int      `json:"event_count"`
	GapOver1S            int      `json:"gap_over_1s"`
	GapOver2S            int      `json:"gap_over_2s"`
	FinishReason         string   `json:"finish_reason"`
	ObservedModel        string   `json:"observed_model"`
	ErrorCode            string   `json:"error_code,omitempty"`
	StreamDone           bool     `json:"stream_done"`
	ProviderCost         *float64 `json:"provider_cost"`
	MeasurementMode      string   `json:"measurement_mode"`
}

type USTCSpeedSample struct {
	ID            int64     `json:"id"`
	LegacyUsageID *int64    `json:"legacy_usage_id,omitempty"`
	TaskID        *int64    `json:"task_id"`
	RoundID       string    `json:"round_id,omitempty"`
	AccountID     int64     `json:"account_id"`
	AccountName   string    `json:"account_name"`
	KeyScope      string    `json:"-"`
	Model         string    `json:"model"`
	Source        string    `json:"source"`
	Status        string    `json:"status"`
	Reason        string    `json:"reason,omitempty"`
	HTTPStatus    int       `json:"http_status"`
	Stream        bool      `json:"stream"`
	APIMode       string    `json:"api_mode"`
	Profile       string    `json:"profile"`
	Reasoning     string    `json:"reasoning"`
	StartedAt     time.Time `json:"started_at"`
	FinishedAt    time.Time `json:"finished_at"`
	USTCSpeedObservation
}

type USTCSpeedTask struct {
	ID                        int64      `json:"id"`
	Revision                  int64      `json:"revision"`
	Name                      string     `json:"name"`
	Enabled                   bool       `json:"enabled"`
	IgnoreTaskConstraints     bool       `json:"ignore_task_constraints"`
	AllAccounts               bool       `json:"all_accounts"`
	AccountIDs                []int64    `json:"account_ids"`
	Models                    []string   `json:"models"`
	IntervalSeconds           int        `json:"interval_seconds"`
	CronExpression            string     `json:"cron_expression"`
	JitterSeconds             int        `json:"jitter_seconds"`
	Timezone                  string     `json:"timezone"`
	ActiveStart               string     `json:"active_start"`
	ActiveEnd                 string     `json:"active_end"`
	DaysOfWeek                []int      `json:"days_of_week"`
	APIMode                   string     `json:"api_mode"`
	Stream                    bool       `json:"stream"`
	Prompt                    string     `json:"prompt"`
	PromptVariants            []string   `json:"prompt_variants"`
	MaxTokens                 int        `json:"max_tokens"`
	Temperature               float64    `json:"temperature"`
	TopP                      *float64   `json:"top_p"`
	Seed                      *int64     `json:"seed"`
	Stop                      []string   `json:"stop"`
	ThinkingMode              string     `json:"thinking_mode"`
	ReasoningEffort           string     `json:"reasoning_effort"`
	TimeoutSeconds            int        `json:"timeout_seconds"`
	FirstOutputTimeoutSeconds int        `json:"first_output_timeout_seconds"`
	MaxParallelAccounts       int        `json:"max_parallel_accounts"`
	IdleModelConcurrency      int        `json:"idle_model_concurrency"`
	MaxStartSkewSeconds       int        `json:"max_start_skew_seconds"`
	ReserveRPM                int        `json:"reserve_rpm"`
	ReserveConcurrency        int        `json:"reserve_concurrency"`
	MaxActiveRequests         int        `json:"max_active_requests"`
	SkipIfUserWaiting         bool       `json:"skip_if_user_waiting"`
	BackoffAfter429           int        `json:"backoff_after_429"`
	BackoffWindowSeconds      int        `json:"backoff_window_seconds"`
	BackoffMultiplier         float64    `json:"backoff_multiplier"`
	MaxIntervalSeconds        int        `json:"max_interval_seconds"`
	PauseAfter429             int        `json:"pause_after_429"`
	RecoverySuccesses         int        `json:"recovery_successes"`
	NextRunAt                 *time.Time `json:"next_run_at"`
	LastRunAt                 *time.Time `json:"last_run_at"`
	LastStatus                string     `json:"last_status"`
	LastDetail                string     `json:"last_detail,omitempty"`
	CreatedAt                 time.Time  `json:"created_at"`
	UpdatedAt                 time.Time  `json:"updated_at"`

	Progress      *USTCSpeedTaskProgress `json:"progress,omitempty"`
	LeaseUntil    *time.Time             `json:"lease_until,omitempty"`
	LeaseRevision *int64                 `json:"-"`
}

type USTCSpeedActiveProbe struct {
	AccountID   int64     `json:"account_id"`
	AccountName string    `json:"account_name"`
	Model       string    `json:"model"`
	Phase       string    `json:"phase"`
	StartedAt   time.Time `json:"started_at"`
}

// Progress counts account/model test items, including skips and cancellations.
// It is operational state, separate from task configuration and speed metrics.
type USTCSpeedTaskProgress struct {
	RunID        string                 `json:"run_id"`
	State        string                 `json:"state"`
	StartedAt    time.Time              `json:"started_at"`
	UpdatedAt    time.Time              `json:"updated_at"`
	FinishedAt   *time.Time             `json:"finished_at,omitempty"`
	Total        int                    `json:"total"`
	Completed    int                    `json:"completed"`
	Successful   int                    `json:"successful"`
	Failed       int                    `json:"failed"`
	Skipped      int                    `json:"skipped"`
	Cancelled    int                    `json:"cancelled"`
	CurrentModel string                 `json:"current_model,omitempty"`
	Reason       string                 `json:"reason,omitempty"`
	Active       []USTCSpeedActiveProbe `json:"active"`
}

type USTCSpeedSettings struct {
	MaxBackgroundConcurrency int `json:"max_background_concurrency"`
}

type USTCSpeedQuery struct {
	Start, End                  time.Time
	Model, Source, Status       string
	GroupBy                     string
	AccountIDs                  []int64
	TaskID                      int64
	Reasoning, Stream, Profile  string
	InputMin, InputMax          *int
	OutputMin, OutputMax        *int
	Metric, Interval, Aggregate string
	Limit, Offset               int
	// Timezone is the caller's IANA zone. Hourly groups the same filtered calls
	// by local time of day so users can pick a faster time to run work.
	Timezone string
	Hourly   bool
	// HourlyBucketMinutes is the width of one folded time-of-day bucket. Zero or
	// an unsupported value falls back to 60, the historical hourly view.
	HourlyBucketMinutes int
	// HourlyMetric selects the speed metric for the hourly view independently of
	// the trend chart. It is limited to TPS metrics; empty falls back to Metric
	// when that is a TPS metric, otherwise to overall_tps.
	HourlyMetric string
	// LastHours switches the range from calendar days to an exact rolling
	// window ending now. Zero keeps the date-based Start/End range.
	LastHours int
	// Analysis derives comparisons and distributions from existing observations.
	Analysis   bool
	Comparison string
	// UsageFilters selects live usage_logs as the read source. No speed samples
	// are imported or written when analyzing real calls.
	UsageFilters *usagestats.UsageLogFilters
}

type USTCSpeedSummary struct {
	Samples       int64      `json:"samples"`
	Successful    int64      `json:"successful"`
	Failed        int64      `json:"failed"`
	RateLimited   int64      `json:"rate_limited"`
	Skipped       int64      `json:"skipped"`
	Cancelled     int64      `json:"cancelled"`
	ProbeSamples  int64      `json:"probe_samples"`
	RealSamples   int64      `json:"real_samples"`
	OutputTokens  int64      `json:"output_tokens"`
	SuccessRate   *float64   `json:"success_rate"`
	LatestAt      *time.Time `json:"latest_at"`
	LatestRealAt  *time.Time `json:"latest_real_at"`
	LatestProbeAt *time.Time `json:"latest_probe_at"`
}

// These counters describe actual benchmark requests. Skipped test items never
// contribute requests, tokens, cost or duration. Nullable totals distinguish
// missing upstream usage/cost metadata from a measured zero.
type USTCSpeedStats struct {
	TotalRequests     int64    `json:"total_requests"`
	TotalInputTokens  *int64   `json:"total_input_tokens"`
	TotalOutputTokens *int64   `json:"total_output_tokens"`
	TotalTokens       *int64   `json:"total_tokens"`
	TotalProviderCost *float64 `json:"total_provider_cost"`
	AverageDurationMS *float64 `json:"average_duration_ms"`
	InputSamples      int64    `json:"input_samples"`
	OutputSamples     int64    `json:"output_samples"`
	CostSamples       int64    `json:"cost_samples"`
	DurationSamples   int64    `json:"duration_samples"`
}

type USTCSpeedPoint struct {
	Time        time.Time  `json:"time"`
	BucketEnd   *time.Time `json:"bucket_end,omitempty"`
	AccountID   int64      `json:"account_id"`
	AccountName string     `json:"account_name"`
	Model       string     `json:"model"`
	Source      string     `json:"source"`
	Profile     string     `json:"profile"`
	RoundID     string     `json:"round_id,omitempty"`
	SampleID    *int64     `json:"sample_id,omitempty"`
	Count       int64      `json:"count"`
	ValidCount  int64      `json:"valid_count"`
	Value       *float64   `json:"value"`
	Mean        *float64   `json:"mean"`
	P10         *float64   `json:"p10"`
	P50         *float64   `json:"p50"`
	P90         *float64   `json:"p90"`
	Min         *float64   `json:"min"`
	Max         *float64   `json:"max"`
}

type USTCSpeedHourPoint struct {
	// Slot indexes the folded time-of-day bucket: slot 0 covers local 00:00 up
	// to the bucket width, and the last slot ends at local midnight.
	Slot       int    `json:"slot"`
	Model      string `json:"model"`
	Source     string `json:"source"`
	Profile    string `json:"profile"`
	Count      int64  `json:"count"`
	ValidCount int64  `json:"valid_count"`
	// DayCount counts distinct local dates with valid observations. Rates and
	// quantiles always pool individual calls; days are never equally weighted.
	DayCount int64    `json:"day_count"`
	Value    *float64 `json:"value"`
	Mean     *float64 `json:"mean"`
	P10      *float64 `json:"p10"`
	P50      *float64 `json:"p50"`
	P90      *float64 `json:"p90"`
	Min      *float64 `json:"min"`
	Max      *float64 `json:"max"`
}

type USTCSpeedOverview struct {
	RequestedSource string               `json:"requested_source"`
	RangeStart      time.Time            `json:"range_start"`
	RangeEnd        time.Time            `json:"range_end"`
	Metric          string               `json:"metric"`
	Unit            string               `json:"unit"`
	Interval        string               `json:"interval"`
	GroupBy         string               `json:"group_by"`
	Aggregate       string               `json:"aggregate"`
	Summary         USTCSpeedSummary     `json:"summary"`
	Stats           *USTCSpeedStats      `json:"stats,omitempty"`
	Series          []USTCSpeedPoint     `json:"series"`
	Hourly          []USTCSpeedHourPoint `json:"hourly,omitempty"`
	HourlyTruncated bool                 `json:"hourly_truncated"`
	Truncated       bool                 `json:"truncated"`
	DroppedSamples  uint64               `json:"dropped_samples"`
	Analysis        *USTCSpeedAnalysis   `json:"analysis,omitempty"`
}

type USTCSpeedRuntime struct {
	Recent429      []int64    `json:"-"`
	AccountID      int64      `json:"account_id"`
	AccountName    string     `json:"account_name"`
	KeyScope       string     `json:"-"`
	Consecutive429 int        `json:"consecutive_429"`
	SuccessStreak  int        `json:"success_streak"`
	NotBefore      *time.Time `json:"not_before"`
	Last429At      *time.Time `json:"last_429_at"`
	Paused         bool       `json:"paused"`
	LeaseUntil     *time.Time `json:"lease_until"`
	LastStatus     string     `json:"last_status"`
}

type USTCSpeedAccountDimension struct {
	ID           int64         `json:"id"`
	KeyAccountID int64         `json:"key_account_id"`
	Name         string        `json:"name"`
	Models       []string      `json:"models"`
	Capacity     *USTCCapacity `json:"capacity,omitempty"`
}

type USTCSpeedDimensions struct {
	Accounts     []USTCSpeedAccountDimension `json:"accounts"`
	Models       []string                    `json:"models"`
	TaskDefaults *USTCSpeedTask              `json:"task_defaults,omitempty"`
}

// SQL persistence is kept behind a small port so runner/collector behavior can
// be tested without a live server or spending real upstream quota.
type USTCSpeedRepository interface {
	GetSettings(context.Context) (*USTCSpeedSettings, error)
	SaveSettings(context.Context, USTCSpeedSettings) error
	Models(context.Context) ([]string, error)
	InsertSample(context.Context, *USTCSpeedSample) error
	Overview(context.Context, USTCSpeedQuery) (*USTCSpeedOverview, error)
	ListSamples(context.Context, USTCSpeedQuery) ([]USTCSpeedSample, int64, error)
	GetSample(context.Context, int64) (*USTCSpeedSample, error)
	ListTasks(context.Context) ([]USTCSpeedTask, error)
	GetTask(context.Context, int64) (*USTCSpeedTask, error)
	SaveTask(context.Context, *USTCSpeedTask) error
	DeleteTask(context.Context, int64) error
	ClaimTask(context.Context, int64, string, time.Duration, bool, int64) (bool, error)
	FinishTask(context.Context, int64, string, time.Time, string, string) error
	UpdateTaskProgress(context.Context, int64, string, *USTCSpeedTaskProgress) error
	AcquireKey(context.Context, string, string, time.Duration, int) (bool, error)
	ReleaseKey(context.Context, string, string) error
	GetRuntime(context.Context, string) (*USTCSpeedRuntime, error)
	SaveRuntime(context.Context, string, *USTCSpeedRuntime) error
}
