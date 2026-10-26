package service

import (
	"context"
	"time"
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
	RealFallback              bool       `json:"real_fallback"`
	RealFallbackMaxAgeSeconds int        `json:"real_fallback_max_age_seconds"`
	RealFallbackMinSamples    int        `json:"real_fallback_min_samples"`
	NextRunAt                 *time.Time `json:"next_run_at"`
	LastRunAt                 *time.Time `json:"last_run_at"`
	LastStatus                string     `json:"last_status"`
	LastDetail                string     `json:"last_detail,omitempty"`
	CreatedAt                 time.Time  `json:"created_at"`
	UpdatedAt                 time.Time  `json:"updated_at"`
}

type USTCSpeedSettings struct {
	CaptureEnabled           bool `json:"capture_enabled"`
	MaxBackgroundConcurrency int  `json:"max_background_concurrency"`
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
	// by local hour of day so users can pick a faster time to run work.
	Timezone string
	Hourly   bool
	// LastHours switches the range from calendar days to an exact rolling
	// window ending now. Zero keeps the date-based Start/End range.
	LastHours int
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

type USTCSpeedPoint struct {
	Time        time.Time `json:"time"`
	AccountID   int64     `json:"account_id"`
	AccountName string    `json:"account_name"`
	Model       string    `json:"model"`
	Source      string    `json:"source"`
	Profile     string    `json:"profile"`
	RoundID     string    `json:"round_id,omitempty"`
	SampleID    *int64    `json:"sample_id,omitempty"`
	Count       int64     `json:"count"`
	ValidCount  int64     `json:"valid_count"`
	Value       *float64  `json:"value"`
	Mean        *float64  `json:"mean"`
	P10         *float64  `json:"p10"`
	P50         *float64  `json:"p50"`
	P90         *float64  `json:"p90"`
	Min         *float64  `json:"min"`
	Max         *float64  `json:"max"`
}

type USTCSpeedHourPoint struct {
	Hour       int      `json:"hour"`
	Model      string   `json:"model"`
	Source     string   `json:"source"`
	Profile    string   `json:"profile"`
	Count      int64    `json:"count"`
	ValidCount int64    `json:"valid_count"`
	Value      *float64 `json:"value"`
	Mean       *float64 `json:"mean"`
	P10        *float64 `json:"p10"`
	P50        *float64 `json:"p50"`
	P90        *float64 `json:"p90"`
	Min        *float64 `json:"min"`
	Max        *float64 `json:"max"`
}

type USTCSpeedOverview struct {
	RequestedSource       string               `json:"requested_source"`
	FallbackApplied       bool                 `json:"fallback_applied"`
	FallbackWindowSeconds int                  `json:"fallback_window_seconds"`
	RangeStart            time.Time            `json:"range_start"`
	RangeEnd              time.Time            `json:"range_end"`
	Metric                string               `json:"metric"`
	Unit                  string               `json:"unit"`
	Interval              string               `json:"interval"`
	GroupBy               string               `json:"group_by"`
	Aggregate             string               `json:"aggregate"`
	Summary               USTCSpeedSummary     `json:"summary"`
	Series                []USTCSpeedPoint     `json:"series"`
	Hourly                []USTCSpeedHourPoint `json:"hourly,omitempty"`
	Truncated             bool                 `json:"truncated"`
	DroppedSamples        uint64               `json:"dropped_samples"`
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
	Accounts []USTCSpeedAccountDimension `json:"accounts"`
	Models   []string                    `json:"models"`
}

// SQL persistence is kept behind a small port so runner/collector behavior can
// be tested without a live server or spending real upstream quota.
type USTCSpeedRepository interface {
	GetSettings(context.Context) (*USTCSpeedSettings, time.Time, error)
	SaveSettings(context.Context, USTCSpeedSettings) error
	ImportUsage(context.Context, time.Time, time.Time, []int64, int) (int64, error)
	ImportRecentUsage(context.Context, time.Time, time.Time, []int64, int) (int64, error)
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
	AcquireKey(context.Context, string, string, time.Duration, int) (bool, error)
	ReleaseKey(context.Context, string, string) error
	GetRuntime(context.Context, string) (*USTCSpeedRuntime, error)
	SaveRuntime(context.Context, string, *USTCSpeedRuntime) error
}
