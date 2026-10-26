package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	return USTCSpeedSettings{MaxBackgroundConcurrency: 4}
}
func DefaultUSTCSpeedTask() USTCSpeedTask {
	return USTCSpeedTask{
		AccountIDs: []int64{}, Models: []string{}, IntervalSeconds: 1800, JitterSeconds: 180, Timezone: "Asia/Shanghai", ActiveStart: "00:00", ActiveEnd: "23:59", DaysOfWeek: []int{}, APIMode: "chat_completions", Stream: true,
		Prompt: "Generate a numbered list of 120 practical suggestions for keeping a personal research notebook. Each item must be one short English sentence. Start immediately with item 1; do not add an introduction or conclusion.", PromptVariants: []string{}, MaxTokens: 512, Temperature: 0, Stop: []string{}, ThinkingMode: "off", TimeoutSeconds: 60, FirstOutputTimeoutSeconds: 30, MaxParallelAccounts: 4, MaxStartSkewSeconds: 5, ReserveRPM: 4, ReserveConcurrency: 2, MaxActiveRequests: 16, SkipIfUserWaiting: true, BackoffAfter429: 3, BackoffWindowSeconds: 900, BackoffMultiplier: 2, MaxIntervalSeconds: 7200, PauseAfter429: 6, RecoverySuccesses: 2,
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
	if t.IntervalSeconds < 60 || t.IntervalSeconds > 86400 || t.JitterSeconds < 0 || (t.CronExpression == "" && t.JitterSeconds >= t.IntervalSeconds) {
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
	return nil
}

type USTCSpeedService struct {
	repo          USTCSpeedRepository
	accounts      AccountRepository
	gateway       *OpenAIGatewayService
	cfg           atomic.Pointer[USTCSpeedSettings]
	stopped       atomic.Bool
	dropped       atomic.Uint64
	ctx           context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup
	jobsMu        sync.Mutex
	jobs          map[int64]context.CancelFunc
	overviewCache speedOverviewCache
	overviewEpoch atomic.Uint64
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
	s.wg.Add(1)
	go s.schedulerLoop()
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
func (s *USTCSpeedService) persist(sample USTCSpeedSample) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.repo.InsertSample(ctx, &sample); err != nil {
		s.dropped.Add(1)
		slog.Warn("ustc_speed_sample_write_failed", "error", err)
	} else {
		s.overviewEpoch.Add(1)
	}
}
func (s *USTCSpeedService) refreshSettings(ctx context.Context) {
	cfg, err := s.repo.GetSettings(ctx)
	if err != nil {
		slog.Warn("ustc_speed_settings_read_failed", "error", err)
		return
	}
	if err = ValidateUSTCSpeedSettings(*cfg); err != nil {
		return
	}
	s.cfg.Store(cfg)
}
func (s *USTCSpeedService) GetSettings(ctx context.Context) (*USTCSpeedSettings, error) {
	cfg, err := s.repo.GetSettings(ctx)
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
	return s.cachedOverview(ctx, q)
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
	var previous *USTCSpeedTask
	if task.ID != 0 {
		var err error
		previous, err = s.repo.GetTask(ctx, task.ID)
		if err != nil {
			return err
		}
	}
	next := nextUSTCSpeedTask(*task, time.Now())
	if previous != nil && previous.NextRunAt != nil && sameUSTCSpeedSchedule(*previous, *task) {
		next = *previous.NextRunAt
	}
	// Enabling a task (or creating one already enabled) should start the first
	// round promptly instead of waiting a whole interval.
	if task.Enabled && (previous == nil || !previous.Enabled) {
		next = time.Now()
	}
	task.NextRunAt = &next
	if err := s.repo.SaveTask(ctx, task); err != nil {
		return err
	}
	if previous != nil && previous.Enabled && !task.Enabled {
		s.cancelJob(task.ID)
	}
	return nil
}

func sameUSTCSpeedSchedule(a, b USTCSpeedTask) bool {
	return a.Enabled == b.Enabled && a.IntervalSeconds == b.IntervalSeconds && a.JitterSeconds == b.JitterSeconds && a.CronExpression == b.CronExpression && a.Timezone == b.Timezone
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
	defaults := DefaultUSTCSpeedTask()
	out.TaskDefaults = &defaults
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

func speedProfileHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}
