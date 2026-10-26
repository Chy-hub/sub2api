package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/robfig/cron/v3"
)

var (
	ErrUSTCSpeedNotFound = infraerrors.NotFound("USTC_SPEED_NOT_FOUND", "speed task or sample not found")
	ErrUSTCSpeedBusy     = infraerrors.Conflict("USTC_SPEED_BUSY", "this detection task is already running")
)

func speedBadRequest(message string) error {
	return infraerrors.BadRequest("USTC_SPEED_INVALID", message)
}
func DefaultUSTCSpeedSettings() USTCSpeedSettings {
	return USTCSpeedSettings{CaptureEnabled: true, MaxBackgroundConcurrency: 4}
}
func DefaultUSTCSpeedTask() USTCSpeedTask {
	return USTCSpeedTask{
		AccountIDs: []int64{}, Models: []string{}, IntervalSeconds: 1800, JitterSeconds: 180, Timezone: "Asia/Shanghai", ActiveStart: "00:00", ActiveEnd: "23:59", DaysOfWeek: []int{}, APIMode: "chat_completions", Stream: true,
		Prompt: "Generate a numbered list of 120 practical suggestions for keeping a personal research notebook. Each item must be one short English sentence. Start immediately with item 1; do not add an introduction or conclusion.", PromptVariants: []string{}, MaxTokens: 512, Temperature: 0, Stop: []string{}, ThinkingMode: "off", TimeoutSeconds: 60, FirstOutputTimeoutSeconds: 30, MaxParallelAccounts: 4, MaxStartSkewSeconds: 5, ReserveRPM: 4, ReserveConcurrency: 2, MaxActiveRequests: 16, SkipIfUserWaiting: true, BackoffAfter429: 3, BackoffWindowSeconds: 900, BackoffMultiplier: 2, MaxIntervalSeconds: 7200, PauseAfter429: 6, RecoverySuccesses: 2, RealFallback: true, RealFallbackMaxAgeSeconds: 900, RealFallbackMinSamples: 1,
	}
}
func ValidateUSTCSpeedSettings(s USTCSpeedSettings) error {
	if s.MaxBackgroundConcurrency < 1 || s.MaxBackgroundConcurrency > 16 {
		return speedBadRequest("invalid background concurrency")
	}
	return nil
}
func ValidateUSTCSpeedTask(t *USTCSpeedTask) error {
	t.Name = strings.TrimSpace(t.Name)
	if len(t.Name) < 1 || len(t.Name) > 200 {
		return speedBadRequest("task name is required (maximum 200 bytes)")
	}
	if len(t.Models) == 0 || len(t.Models) > 20 {
		return speedBadRequest("select 1 to 20 models")
	}
	seen := map[string]bool{}
	models := []string{}
	for _, model := range t.Models {
		model = strings.TrimSpace(model)
		if model == "" || len(model) > 200 || strings.ContainsAny(model, "\r\n\x00") {
			return speedBadRequest("invalid model name")
		}
		if !seen[model] {
			models = append(models, model)
			seen[model] = true
		}
	}
	t.Models = models
	if !t.AllAccounts && len(t.AccountIDs) == 0 {
		return speedBadRequest("select accounts or all_accounts")
	}
	if len(t.AccountIDs) > 100 {
		return speedBadRequest("maximum 100 accounts")
	}
	for _, id := range t.AccountIDs {
		if id <= 0 {
			return speedBadRequest("invalid account id")
		}
	}
	if t.IntervalSeconds < 60 || t.IntervalSeconds > 86400 || t.JitterSeconds < 0 || t.JitterSeconds >= t.IntervalSeconds {
		return speedBadRequest("invalid interval or jitter")
	}
	if t.CronExpression != "" {
		if strings.Contains(strings.ToUpper(t.CronExpression), "TZ=") {
			return speedBadRequest("use the timezone field for cron schedules")
		}
		schedule, err := cron.ParseStandard(t.CronExpression)
		if err != nil {
			return speedBadRequest("invalid cron expression")
		}
		first := schedule.Next(time.Now())
		if schedule.Next(first).Sub(first) < time.Minute {
			return speedBadRequest("cron interval must be at least one minute")
		}
	}
	if _, err := time.LoadLocation(t.Timezone); err != nil {
		return speedBadRequest("invalid timezone")
	}
	for _, value := range []string{t.ActiveStart, t.ActiveEnd} {
		if _, err := time.Parse("15:04", value); err != nil {
			return speedBadRequest("active hours must be HH:MM")
		}
	}
	for _, day := range t.DaysOfWeek {
		if day < 0 || day > 6 {
			return speedBadRequest("days_of_week must be 0..6")
		}
	}
	if t.APIMode != "chat_completions" && t.APIMode != "responses" {
		return speedBadRequest("unsupported api_mode")
	}
	if strings.TrimSpace(t.Prompt) == "" || len(t.Prompt) > 128000 || len(t.PromptVariants) > 16 {
		return speedBadRequest("invalid prompt or prompt variants")
	}
	for _, prompt := range t.PromptVariants {
		if strings.TrimSpace(prompt) == "" || len(prompt) > 128000 {
			return speedBadRequest("invalid prompt variant")
		}
	}
	if t.MaxTokens < 16 || t.MaxTokens > 8192 || math.IsNaN(t.Temperature) || t.Temperature < 0 || t.Temperature > 2 {
		return speedBadRequest("invalid generation parameters")
	}
	if t.TopP != nil && (math.IsNaN(*t.TopP) || *t.TopP <= 0 || *t.TopP > 1) {
		return speedBadRequest("top_p must be in (0,1]")
	}
	if len(t.Stop) > 8 {
		return speedBadRequest("too many stop sequences")
	}
	for _, stop := range t.Stop {
		if stop == "" || len(stop) > 1000 {
			return speedBadRequest("invalid stop sequence")
		}
	}
	if t.ThinkingMode != "off" && t.ThinkingMode != "on" && t.ThinkingMode != "auto" {
		return speedBadRequest("thinking_mode must be off, on or auto")
	}
	switch t.ReasoningEffort {
	case "", "low", "medium", "high", "xhigh":
	default:
		return speedBadRequest("invalid reasoning_effort")
	}
	if t.TimeoutSeconds < 5 || t.TimeoutSeconds > 180 || t.FirstOutputTimeoutSeconds < 1 || t.FirstOutputTimeoutSeconds > t.TimeoutSeconds {
		return speedBadRequest("invalid request timeouts")
	}
	if t.MaxParallelAccounts < 1 || t.MaxParallelAccounts > 16 || t.MaxStartSkewSeconds < 1 || t.MaxStartSkewSeconds > 30 || t.ReserveRPM < 0 || t.ReserveConcurrency < 1 || t.MaxActiveRequests < 0 {
		return speedBadRequest("invalid background admission limits")
	}
	if t.BackoffAfter429 < 1 || t.BackoffAfter429 > 20 || t.BackoffWindowSeconds < 60 || t.BackoffWindowSeconds > 86400 || t.BackoffMultiplier < 1 || t.BackoffMultiplier > 10 || t.MaxIntervalSeconds < t.IntervalSeconds || t.MaxIntervalSeconds > 86400 || t.PauseAfter429 < t.BackoffAfter429 || t.PauseAfter429 > 40 || t.RecoverySuccesses < 1 || t.RecoverySuccesses > 20 {
		return speedBadRequest("invalid 429 backoff settings")
	}
	if t.RealFallbackMaxAgeSeconds < 60 || t.RealFallbackMaxAgeSeconds > 86400 || t.RealFallbackMinSamples < 1 || t.RealFallbackMinSamples > 1000 {
		return speedBadRequest("invalid real data fallback settings")
	}
	return nil
}

type USTCSpeedService struct {
	repo           USTCSpeedRepository
	accounts       AccountRepository
	gateway        *OpenAIGatewayService
	cfg            atomic.Pointer[USTCSpeedSettings]
	stopped        atomic.Bool
	dropped        atomic.Uint64
	ctx            context.Context
	cancel         context.CancelFunc
	wg             sync.WaitGroup
	jobsMu         sync.Mutex
	jobs           map[int64]context.CancelFunc
	historyScope   string
	historyDone    bool
	recentCaughtUp bool
	captureSince   atomic.Int64
}

func NewUSTCSpeedService(repo USTCSpeedRepository, accounts AccountRepository, gateway *OpenAIGatewayService) *USTCSpeedService {
	ctx, cancel := context.WithCancel(context.Background())
	s := &USTCSpeedService{repo: repo, accounts: accounts, gateway: gateway, ctx: ctx, cancel: cancel, jobs: map[int64]context.CancelFunc{}}
	cfg := DefaultUSTCSpeedSettings()
	s.cfg.Store(&cfg)
	return s
}
func (s *USTCSpeedService) Start() {
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	s.refreshSettings(ctx)
	cancel()
	s.wg.Add(2)
	go s.schedulerLoop()
	go s.historyLoop()
}
func (s *USTCSpeedService) Stop() {
	if s.stopped.Swap(true) {
		return
	}
	s.cancel()
	s.jobsMu.Lock()
	for _, cancel := range s.jobs {
		cancel()
	}
	s.jobsMu.Unlock()
	s.wg.Wait()
}
func (s *USTCSpeedService) captureEnabled() bool {
	return s != nil && !s.stopped.Load() && s.cfg.Load().CaptureEnabled
}
func (s *USTCSpeedService) persist(sample USTCSpeedSample) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.repo.InsertSample(ctx, &sample); err != nil {
		s.dropped.Add(1)
		slog.Warn("ustc_speed_sample_write_failed", "error", err)
	}
}
func (s *USTCSpeedService) refreshSettings(ctx context.Context) {
	cfg, since, err := s.repo.GetSettings(ctx)
	if err != nil {
		slog.Warn("ustc_speed_settings_read_failed", "error", err)
		return
	}
	if err = ValidateUSTCSpeedSettings(*cfg); err != nil {
		return
	}
	s.cfg.Store(cfg)
	s.captureSince.Store(since.UnixNano())
}
func (s *USTCSpeedService) GetSettings(ctx context.Context) (*USTCSpeedSettings, error) {
	cfg, _, err := s.repo.GetSettings(ctx)
	return cfg, err
}
func (s *USTCSpeedService) SaveSettings(ctx context.Context, cfg USTCSpeedSettings) error {
	if err := ValidateUSTCSpeedSettings(cfg); err != nil {
		return err
	}
	if err := s.repo.SaveSettings(ctx, cfg); err != nil {
		return err
	}
	s.cfg.Store(&cfg)
	return nil
}
func (s *USTCSpeedService) Overview(ctx context.Context, q USTCSpeedQuery) (*USTCSpeedOverview, error) {
	out, err := s.repo.Overview(ctx, q)
	if err != nil || out == nil {
		return out, err
	}
	out.RequestedSource = q.Source
	out.RangeStart = q.Start
	out.RangeEnd = q.End
	out.DroppedSamples = s.dropped.Load()
	// A historical benchmark query must never be silently replaced by today's
	// traffic. Fallback is only for a current, paused/stale benchmark view.
	now := time.Now()
	if q.Source != "probe" || !q.End.After(now) || q.Start.After(now) {
		return out, nil
	}
	tasks, err := s.repo.ListTasks(ctx)
	if err != nil {
		return out, nil
	}
	var policy *USTCSpeedTask
	for i := range tasks {
		task := &tasks[i]
		if !task.RealFallback || (q.TaskID > 0 && task.ID != q.TaskID) {
			continue
		}
		modelMatches := q.Model == ""
		for _, model := range task.Models {
			if model == q.Model {
				modelMatches = true
				break
			}
		}
		if modelMatches {
			policy = task
			break
		}
	}
	if policy == nil {
		return out, nil
	}
	if out.Summary.LatestProbeAt != nil && now.Sub(*out.Summary.LatestProbeAt) <= time.Duration(policy.RealFallbackMaxAgeSeconds)*time.Second {
		return out, nil
	}
	blocked := out.Summary.Skipped > 0
	if !blocked {
		accounts, err := s.ustcAccounts(ctx)
		if err != nil {
			return out, nil
		}
		selected := map[int64]bool{}
		for _, id := range q.AccountIDs {
			selected[id] = true
		}
		for _, account := range accounts {
			if len(selected) > 0 && !selected[account.ID] {
				continue
			}
			runtime, err := s.repo.GetRuntime(ctx, USTCKeyScope(&account))
			if err == nil && (runtime.Paused || (runtime.NotBefore != nil && runtime.NotBefore.After(now))) {
				blocked = true
				break
			}
		}
	}
	if !blocked {
		return out, nil
	}
	realQuery := q
	realQuery.Source = "real"
	realQuery.TaskID = 0
	realQuery.Profile = ""
	fresh := now.Add(-time.Duration(policy.RealFallbackMaxAgeSeconds) * time.Second)
	if realQuery.Start.Before(fresh) {
		realQuery.Start = fresh
	}
	if len(realQuery.AccountIDs) == 0 && !policy.AllAccounts {
		realQuery.AccountIDs = append([]int64(nil), policy.AccountIDs...)
	}
	real, err := s.repo.Overview(ctx, realQuery)
	if err != nil || real == nil {
		return out, nil
	}
	var validSamples int64
	for _, point := range real.Series {
		if point.Value != nil {
			validSamples += point.ValidCount
		}
	}
	if validSamples < int64(policy.RealFallbackMinSamples) {
		return out, nil
	}
	real.RequestedSource = q.Source
	real.FallbackApplied = true
	real.FallbackWindowSeconds = policy.RealFallbackMaxAgeSeconds
	real.RangeStart = q.Start
	real.RangeEnd = q.End
	real.DroppedSamples = s.dropped.Load()
	return real, nil
}
func (s *USTCSpeedService) ListSamples(ctx context.Context, q USTCSpeedQuery) ([]USTCSpeedSample, int64, error) {
	return s.repo.ListSamples(ctx, q)
}
func (s *USTCSpeedService) GetSample(ctx context.Context, id int64) (*USTCSpeedSample, error) {
	return s.repo.GetSample(ctx, id)
}
func (s *USTCSpeedService) ListTasks(ctx context.Context) ([]USTCSpeedTask, error) {
	return s.repo.ListTasks(ctx)
}
func (s *USTCSpeedService) GetTask(ctx context.Context, id int64) (*USTCSpeedTask, error) {
	return s.repo.GetTask(ctx, id)
}
func (s *USTCSpeedService) SaveTask(ctx context.Context, task *USTCSpeedTask) error {
	if err := ValidateUSTCSpeedTask(task); err != nil {
		return err
	}
	if !task.AllAccounts {
		accounts, err := s.accounts.GetByIDs(ctx, task.AccountIDs)
		if err != nil {
			return err
		}
		found := map[int64]bool{}
		for _, account := range accounts {
			if !IsUSTCCapacityAccount(account) {
				return speedBadRequest("only USTC OpenAI API-key accounts can be benchmarked")
			}
			found[account.ID] = true
		}
		for _, id := range task.AccountIDs {
			if !found[id] {
				return speedBadRequest("selected account does not exist")
			}
		}
	}
	previouslyEnabled := false
	if task.ID != 0 {
		if previous, err := s.repo.GetTask(ctx, task.ID); err == nil && previous != nil {
			previouslyEnabled = previous.Enabled
		}
	}
	next := nextUSTCSpeedTask(*task, time.Now())
	// Enabling a task (or creating one already enabled) should start the first
	// round promptly instead of waiting a whole interval.
	if task.Enabled && !previouslyEnabled {
		next = time.Now()
	}
	task.NextRunAt = &next
	if err := s.repo.SaveTask(ctx, task); err != nil {
		return err
	}
	s.cancelJob(task.ID)
	return nil
}
func (s *USTCSpeedService) DeleteTask(ctx context.Context, id int64) error {
	s.cancelJob(id)
	return s.repo.DeleteTask(ctx, id)
}
func (s *USTCSpeedService) cancelJob(id int64) {
	s.jobsMu.Lock()
	if cancel := s.jobs[id]; cancel != nil {
		cancel()
	}
	s.jobsMu.Unlock()
}
func (s *USTCSpeedService) ustcAccounts(ctx context.Context) ([]Account, error) {
	accounts, err := s.accounts.ListByPlatform(ctx, PlatformOpenAI)
	if err != nil {
		return nil, err
	}
	out := []Account{}
	for _, a := range accounts {
		if IsUSTCCapacityAccount(&a) {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (s *USTCSpeedService) Dimensions(ctx context.Context) (*USTCSpeedDimensions, error) {
	accounts, err := s.ustcAccounts(ctx)
	if err != nil {
		return nil, err
	}
	models, err := s.repo.Models(ctx)
	if err != nil {
		return nil, err
	}
	modelSet := map[string]bool{}
	for _, m := range models {
		modelSet[m] = true
	}
	out := &USTCSpeedDimensions{Accounts: []USTCSpeedAccountDimension{}, Models: []string{}}
	canonical := map[string]int64{}
	for _, a := range accounts {
		scope := USTCKeyScope(&a)
		if canonical[scope] == 0 {
			canonical[scope] = a.ID
		}
		item := USTCSpeedAccountDimension{ID: a.ID, KeyAccountID: canonical[scope], Name: a.Name, Models: []string{}}
		accountModels := map[string]bool{}
		for requested, mapped := range a.GetModelMapping() {
			for _, model := range []string{requested, mapped} {
				if model != "" && !strings.ContainsAny(model, "*?") && !accountModels[model] {
					item.Models = append(item.Models, model)
					modelSet[model] = true
					accountModels[model] = true
				}
			}
		}
		sort.Strings(item.Models)
		out.Accounts = append(out.Accounts, item)
	}
	if s.gateway != nil && s.gateway.rpmCache != nil {
		caps := USTCAccountCapacitiesBatch(ctx, s.gateway.rpmCache, accounts)
		for i := range out.Accounts {
			out.Accounts[i].Capacity = caps[out.Accounts[i].ID]
		}
	}
	for model := range modelSet {
		out.Models = append(out.Models, model)
	}
	sort.Strings(out.Models)
	return out, nil
}
func (s *USTCSpeedService) Runtime(ctx context.Context) ([]USTCSpeedRuntime, error) {
	accounts, err := s.ustcAccounts(ctx)
	if err != nil {
		return nil, err
	}
	out := []USTCSpeedRuntime{}
	cache := map[string]*USTCSpeedRuntime{}
	for _, a := range accounts {
		scope := USTCKeyScope(&a)
		runtime := cache[scope]
		if runtime == nil {
			runtime, err = s.repo.GetRuntime(ctx, scope)
			if err != nil {
				return nil, err
			}
			cache[scope] = runtime
		}
		item := *runtime
		item.AccountID = a.ID
		item.AccountName = a.Name
		out = append(out, item)
	}
	return out, nil
}

// historyLoop imports old usage in bounded pages on its own schedule. Keeping
// it out of schedulerLoop means a large first-time import cannot delay work
// that is due to start.
func (s *USTCSpeedService) historyLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	turn := 0
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			turn++
			ctx, cancel := context.WithTimeout(s.ctx, 2*time.Minute)
			if turn%10 == 1 || !s.recentCaughtUp {
				s.importRecentUsage(ctx)
			}
			s.importHistory(ctx)
			cancel()
		}
	}
}

// importRecentUsage is the only source of new real-call samples: user traffic
// is intentionally not observed on the gateway, so speed metrics reuse the
// duration, first-token, and token counts already stored in usage records. A
// rolling 24-hour window catches up after restarts while keeping each pass
// bounded; imported rows are deduplicated by usage-log ID.
func (s *USTCSpeedService) importRecentUsage(ctx context.Context) {
	if !s.captureEnabled() {
		return
	}
	accounts, err := s.ustcAccounts(ctx)
	if err != nil || len(accounts) == 0 {
		return
	}
	ids := make([]int64, 0, len(accounts))
	for _, account := range accounts {
		ids = append(ids, account.ID)
	}
	now := time.Now()
	since := now.Add(-24 * time.Hour)
	if !s.recentCaughtUp {
		// After an upgrade the last passively captured sample may be older than
		// the steady window. Walk newest-first from the feature's start until
		// fewer than one full page remains.
		if capture := s.captureSince.Load(); capture > 0 {
			since = time.Unix(0, capture)
		}
	}
	count, err := s.repo.ImportRecentUsage(ctx, now, since, ids, 2000)
	if err != nil {
		slog.Warn("ustc_speed_recent_import_failed", "error", err)
		return
	}
	if !s.recentCaughtUp && count < 2000 {
		s.recentCaughtUp = true
	}
	if count > 0 {
		slog.Info("ustc_speed_recent_import", "count", count)
	}
}

func (s *USTCSpeedService) importHistory(ctx context.Context) {
	// Zero time and pre-epoch values both mean "capture start is not known".
	since := s.captureSince.Load()
	if !s.captureEnabled() || since <= 0 {
		return
	}
	accounts, err := s.ustcAccounts(ctx)
	if err != nil {
		return
	}
	ids := []int64{}
	for _, a := range accounts {
		ids = append(ids, a.ID)
	}
	scope := fmt.Sprint(ids)
	if scope != s.historyScope {
		s.historyScope = scope
		s.historyDone = false
	}
	if s.historyDone {
		return
	}
	count, err := s.repo.ImportUsage(ctx, time.Unix(0, since), time.Time{}, ids, 2000)
	if err != nil {
		slog.Warn("ustc_speed_history_import_failed", "error", err)
		return
	}
	s.historyDone = count < 2000
}
func speedProfileHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}
