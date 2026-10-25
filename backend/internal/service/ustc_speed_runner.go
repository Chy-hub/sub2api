package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
)

var errUSTCSpeedFirstTimeout = errors.New("ustc speed first-output timeout")

type USTCSpeedWaitReader interface {
	USTCHasWaiting(context.Context) (bool, error)
}

func (s *USTCSpeedService) schedulerLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	turn := 0
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			turn++
			ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
			if turn%6 == 1 {
				s.refreshSettings(ctx)
				s.importHistory(ctx)
			}
			if turn%60 == 1 {
				_ = s.repo.Cleanup(ctx, time.Now().Add(-time.Duration(s.cfg.Load().RetentionDays)*24*time.Hour))
			}
			tasks, err := s.repo.ListTasks(ctx)
			if err == nil {
				for i := range tasks {
					task := tasks[i]
					if task.Enabled && task.NextRunAt != nil && !task.NextRunAt.After(time.Now()) {
						_, _ = s.startTask(ctx, task, false)
					}
				}
			}
			cancel()
		}
	}
}
func (s *USTCSpeedService) RunTask(ctx context.Context, id int64) (string, error) {
	task, err := s.repo.GetTask(ctx, id)
	if err != nil {
		return "", err
	}
	return s.startTask(ctx, *task, true)
}
func (s *USTCSpeedService) startTask(ctx context.Context, task USTCSpeedTask, force bool) (string, error) {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	if s.stopped.Load() {
		return "", ErrUSTCSpeedBusy
	}
	if _, exists := s.jobs[task.ID]; exists {
		return "", ErrUSTCSpeedBusy
	}
	owner := uuid.NewString()
	ttl := time.Duration(len(task.Models)*(task.TimeoutSeconds+task.MaxStartSkewSeconds+15)+60) * time.Second
	claimed, err := s.repo.ClaimTask(ctx, task.ID, owner, ttl, force, task.Revision)
	if err != nil {
		return "", err
	}
	if !claimed {
		return "", ErrUSTCSpeedBusy
	}
	if s.stopped.Load() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = s.repo.FinishTask(cleanup, task.ID, owner, time.Now().Add(time.Duration(task.IntervalSeconds)*time.Second), "cancelled")
		return "", ErrUSTCSpeedBusy
	}
	jobCtx, cancel := context.WithTimeout(s.ctx, ttl-5*time.Second)
	s.jobs[task.ID] = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel()
		defer func() { s.jobsMu.Lock(); delete(s.jobs, task.ID); s.jobsMu.Unlock() }()
		status := s.executeTask(jobCtx, task, owner)
		next := nextUSTCSpeedTask(task, time.Now())
		finishCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_ = s.repo.FinishTask(finishCtx, task.ID, owner, next, status)
	}()
	return owner, nil
}

func (s *USTCSpeedService) speedTaskCurrent(ctx context.Context, task USTCSpeedTask) bool {
	if ctx.Err() != nil {
		return false
	}
	if task.Revision == 0 {
		return true // Direct collector/runner fixtures have no persisted task.
	}
	current, err := s.repo.GetTask(ctx, task.ID)
	return err == nil && current != nil && current.Revision == task.Revision
}

func nextUSTCSpeedTask(task USTCSpeedTask, now time.Time) time.Time {
	if task.CronExpression != "" {
		if schedule, err := cron.ParseStandard(task.CronExpression); err == nil {
			location, err := time.LoadLocation(task.Timezone)
			if err == nil {
				next := schedule.Next(now.In(location))
				if task.JitterSeconds > 0 {
					next = next.Add(time.Duration(rand.IntN(task.JitterSeconds+1)) * time.Second)
				}
				return next
			}
		}
	}
	next := now.Add(time.Duration(task.IntervalSeconds) * time.Second)
	if task.JitterSeconds > 0 {
		next = next.Add(time.Duration(rand.IntN(2*task.JitterSeconds+1)-task.JitterSeconds) * time.Second)
	}
	return next
}
func speedTaskActive(task USTCSpeedTask, now time.Time) bool {
	location, err := time.LoadLocation(task.Timezone)
	if err != nil {
		return false
	}
	now = now.In(location)
	if len(task.DaysOfWeek) > 0 {
		allowed := false
		for _, day := range task.DaysOfWeek {
			if int(now.Weekday()) == day {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}
	start, err := time.Parse("15:04", task.ActiveStart)
	if err != nil {
		return false
	}
	end, err := time.Parse("15:04", task.ActiveEnd)
	if err != nil {
		return false
	}
	minute := now.Hour()*60 + now.Minute()
	lo, hi := start.Hour()*60+start.Minute(), end.Hour()*60+end.Minute()
	if lo == hi {
		return true
	}
	if lo < hi {
		return minute >= lo && minute <= hi
	}
	return minute >= lo || minute <= hi
}
func (s *USTCSpeedService) executeTask(ctx context.Context, task USTCSpeedTask, owner string) string {
	if !speedTaskActive(task, time.Now()) {
		return "outside_active_window"
	}
	accounts, err := s.ustcAccounts(ctx)
	if err != nil {
		return "failed"
	}
	selected := map[int64]bool{}
	for _, id := range task.AccountIDs {
		selected[id] = true
	}
	seen := map[string]bool{}
	eligible := []Account{}
	for _, account := range accounts {
		if !task.AllAccounts && !selected[account.ID] {
			continue
		}
		scope := USTCKeyScope(&account)
		if seen[scope] {
			continue
		}
		seen[scope] = true
		eligible = append(eligible, account)
	}
	if len(eligible) == 0 {
		return "no_accounts"
	}
	sort.Slice(eligible, func(i, j int) bool { return eligible[i].ID < eligible[j].ID })
	// Rotate the start order when the configured global cap cannot cover every
	// Key inside the paired-round start window, rather than starving the tail.
	if len(eligible) > 1 {
		offset := rand.IntN(len(eligible))
		eligible = append(eligible[offset:], eligible[:offset]...)
	}
	successes, failures, skipped := 0, 0, 0
	for modelIndex, model := range task.Models {
		if !s.speedTaskCurrent(ctx, task) {
			return "cancelled"
		}
		round := owner + fmt.Sprintf("-%d", modelIndex)
		roundStart := time.Now()
		prompt := task.Prompt
		if len(task.PromptVariants) > 0 {
			prompt = task.PromptVariants[rand.IntN(len(task.PromptVariants))]
		}
		semaphore := make(chan struct{}, task.MaxParallelAccounts)
		results := make(chan string, len(eligible))
		var workers sync.WaitGroup
		for _, candidate := range eligible {
			account := candidate
			workers.Add(1)
			go func() {
				defer workers.Done()
				deadline := time.NewTimer(time.Until(roundStart.Add(time.Duration(task.MaxStartSkewSeconds) * time.Second)))
				defer deadline.Stop()
				select {
				case semaphore <- struct{}{}:
					defer func() { <-semaphore }()
					results <- s.runProbe(ctx, task, &account, model, prompt, round, roundStart)
				case <-deadline.C:
					s.recordSkip(task, &account, model, round, "round_start_skew")
					results <- "skipped"
				case <-ctx.Done():
					results <- "cancelled"
				}
			}()
		}
		workers.Wait()
		close(results)
		for result := range results {
			switch result {
			case "success":
				successes++
			case "skipped":
				skipped++
			case "cancelled":
				if ctx.Err() != nil {
					return "cancelled"
				}
				failures++
			default:
				failures++
			}
		}
	}
	if failures > 0 {
		if successes > 0 {
			return "partial"
		}
		return "failed"
	}
	if successes == 0 && skipped > 0 {
		return "skipped"
	}
	return "completed"
}
func (s *USTCSpeedService) recordSkip(task USTCSpeedTask, account *Account, model, round, reason string) {
	now := time.Now()
	sample := USTCSpeedSample{TaskID: &task.ID, RoundID: round, AccountID: account.ID, AccountName: account.Name, KeyScope: USTCKeyScope(account), Model: account.GetMappedModel(model), Source: "probe", Status: "skipped", Reason: reason, Stream: task.Stream, APIMode: task.APIMode, Profile: "probe:skipped", Reasoning: "unknown", StartedAt: now, FinishedAt: now}
	if task.RealFallback {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		q := USTCSpeedQuery{Start: now.Add(-time.Duration(task.RealFallbackMaxAgeSeconds) * time.Second), End: now.Add(time.Nanosecond), Model: sample.Model, Source: "real", AccountIDs: []int64{account.ID}, Limit: 1}
		_, total, err := s.repo.ListSamples(ctx, q)
		cancel()
		if err == nil && total >= int64(task.RealFallbackMinSamples) {
			sample.Reason += "_using_real_data"
		}
	}
	s.persist(sample)
}
func speedBackoff(task USTCSpeedTask, runtime *USTCSpeedRuntime, now time.Time) (bool, string) {
	if runtime == nil {
		return false, ""
	}
	count := 0
	for _, at := range runtime.Recent429 {
		if now.Sub(time.UnixMilli(at)) <= time.Duration(task.BackoffWindowSeconds)*time.Second {
			count++
		}
	}
	if runtime.SuccessStreak >= task.RecoverySuccesses {
		runtime.Paused = false
		runtime.NotBefore = nil
		return false, ""
	}
	if count >= task.BackoffAfter429 && runtime.Last429At != nil {
		factor := math.Pow(task.BackoffMultiplier, float64(count-task.BackoffAfter429+1))
		seconds := math.Min(float64(task.MaxIntervalSeconds), float64(task.IntervalSeconds)*factor)
		if count >= task.PauseAfter429 {
			runtime.Paused = true
			seconds = float64(task.MaxIntervalSeconds)
		}
		next := runtime.Last429At.Add(time.Duration(seconds) * time.Second)
		if runtime.NotBefore == nil || runtime.NotBefore.Before(next) {
			runtime.NotBefore = &next
		}
	}
	if runtime.NotBefore != nil && runtime.NotBefore.After(now) {
		if runtime.Paused {
			return true, "429_paused"
		}
		return true, "429_backoff"
	}
	// An expired pause allows at most the one holder of the shared Key lease;
	// the normal USTC gate still owns cooldown and verify-one admission.
	return false, ""
}
func (s *OpenAIGatewayService) speedProbeHasHeadroom(ctx context.Context, account *Account, task USTCSpeedTask, reserved bool) bool {
	if s == nil || account == nil || !account.IsSchedulable() {
		return false
	}
	if userInfoQuotaSchedulingFailureReason(account, time.Now()) != "" {
		return false
	}
	limits, known := USTCAccountLimits(account)
	cache, ok := s.rpmCache.(USTCCapacityCache)
	if !known || !ok {
		return false
	}
	capacity, err := cache.USTCRead(ctx, USTCKeyScope(account), limits)
	if err != nil || (!reserved && capacity.Available < 1 && (limits.RPM > 0 || limits.Parallel > 0)) || capacity.State != "ready" && capacity.State != "verify_one" {
		return false
	}
	extra := 1
	if reserved {
		extra = 0
	}
	if limits.RPM > 0 && limits.RPM-capacity.Used-capacity.Pending < task.ReserveRPM+extra {
		return false
	}
	if limits.Parallel > 0 && limits.Parallel-capacity.InFlight < task.ReserveConcurrency+extra {
		return false
	}
	if task.MaxActiveRequests > 0 && capacity.InFlight+extra > task.MaxActiveRequests {
		return false
	}
	if task.SkipIfUserWaiting {
		if reader, ok := s.rpmCache.(USTCSpeedWaitReader); ok {
			waiting, err := reader.USTCHasWaiting(ctx)
			if err != nil || waiting {
				return false
			}
		}
		if s.concurrencyService != nil {
			waiting, err := s.concurrencyService.GetAccountWaitingCount(ctx, account.ID)
			if err != nil || waiting > 0 {
				return false
			}
		}
		state := s.defaultUSTCPoolState()
		state.mu.Lock()
		waiting := false
		for _, count := range state.waiters {
			if count > 0 {
				waiting = true
				break
			}
		}
		if !waiting {
			for _, coordinator := range state.coordinators {
				for _, job := range coordinator.jobs {
					if !job.done && job.ctx.Err() == nil {
						waiting = true
						break
					}
				}
				if waiting {
					break
				}
			}
		}
		state.mu.Unlock()
		if waiting {
			return false
		}
	}
	return true
}
func speedProbeBody(task USTCSpeedTask, model, prompt string) ([]byte, error) {
	body := map[string]any{"model": model, "stream": task.Stream, "temperature": task.Temperature}
	if task.APIMode == "responses" {
		body["input"] = prompt
		body["max_output_tokens"] = task.MaxTokens
		if task.ThinkingMode != "auto" || task.ReasoningEffort != "" {
			effort := task.ReasoningEffort
			if task.ThinkingMode == "off" {
				effort = "none"
			} else if effort == "" {
				effort = "medium"
			}
			body["reasoning"] = map[string]string{"effort": effort}
		}
	} else {
		body["messages"] = []map[string]string{{"role": "user", "content": prompt}}
		body["max_tokens"] = task.MaxTokens
		if task.Stream {
			body["stream_options"] = map[string]bool{"include_usage": true}
		}
		if task.ThinkingMode != "auto" {
			mode := "disabled"
			if task.ThinkingMode == "on" {
				mode = "enabled"
			}
			body["thinking"] = map[string]string{"type": mode}
		}
		if task.ReasoningEffort != "" {
			body["reasoning_effort"] = task.ReasoningEffort
		}
	}
	if task.TopP != nil {
		body["top_p"] = *task.TopP
	}
	if task.Seed != nil {
		body["seed"] = *task.Seed
	}
	if len(task.Stop) > 0 {
		body["stop"] = task.Stop
	}
	return json.Marshal(body)
}
func speedProbeURL(account *Account, mode string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(account.GetOpenAIBaseURL()), "/")
	if !strings.Contains(base, "://") {
		base = "https://" + base
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Hostname(), "api.llm.ustc.edu.cn") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", speedBadRequest("invalid USTC account endpoint")
	}
	if !strings.HasSuffix(parsed.Path, "/v1") {
		parsed.Path = strings.TrimRight(parsed.Path, "/") + "/v1"
	}
	if mode == "responses" {
		parsed.Path += "/responses"
	} else {
		parsed.Path += "/chat/completions"
	}
	return parsed.String(), nil
}
func (s *USTCSpeedService) runProbe(parent context.Context, task USTCSpeedTask, account *Account, model, prompt, round string, roundStart time.Time) string {
	skip := func(reason string) string { s.recordSkip(task, account, model, round, reason); return "skipped" }
	if !s.speedTaskCurrent(parent, task) {
		return "cancelled"
	}
	if s.gateway == nil {
		return skip("gateway_unavailable")
	}
	if !account.IsModelSupported(model) {
		return skip("model_not_supported")
	}
	runtime, err := s.repo.GetRuntime(parent, USTCKeyScope(account))
	if err != nil {
		return skip("runtime_unavailable")
	}
	blocked, reason := speedBackoff(task, runtime, time.Now())
	if err = s.repo.SaveRuntime(parent, USTCKeyScope(account), runtime); err != nil {
		return skip("runtime_unavailable")
	}
	if blocked {
		return skip(reason)
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(task.TimeoutSeconds)*time.Second)
	defer cancel()
	ctx, cancelCause := context.WithCancelCause(ctx)
	defer cancelCause(nil)
	current := s.gateway.ustcAccountForAdmission(ctx, account)
	if current == nil {
		return skip("limits_unknown")
	}
	account = current
	if !s.gateway.speedProbeHasHeadroom(ctx, account, task, false) {
		return skip("user_load_or_capacity")
	}
	owner := round + fmt.Sprintf(":%d", account.ID)
	acquired, err := s.repo.AcquireKey(ctx, USTCKeyScope(account), owner, time.Duration(task.TimeoutSeconds+15)*time.Second, s.cfg.Load().MaxBackgroundConcurrency)
	if err != nil || !acquired {
		return skip("background_capacity")
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 3*time.Second)
		defer done()
		_ = s.repo.ReleaseKey(cleanup, USTCKeyScope(account), owner)
	}()
	selection := &AccountSelectionResult{Account: account}
	if !s.gateway.PrepareUSTCAdmission(ctx, selection) {
		return skip("shared_admission")
	}
	defer selection.ReleaseUSTCAdmission()
	account = selection.Account
	plannedRequests, plannedOutput := 1, task.MaxTokens
	if selection.ustcAdmission.ticket.Cold && task.Stream {
		plannedRequests++
		plannedOutput++
	}
	cfg := s.cfg.Load()
	allowed, err := s.repo.ReserveBudget(ctx, task.ID, plannedRequests, plannedOutput, task.MaxDailyRequests, task.MaxDailyOutputTokens, cfg.DailyRequestBudget, cfg.DailyOutputTokenBudget)
	if err != nil || !allowed {
		return skip("daily_budget")
	}
	mapped := account.GetMappedModel(model)
	body, err := speedProbeBody(task, mapped, prompt)
	if err != nil {
		return skip("invalid_request")
	}
	endpoint, err := speedProbeURL(account, task.APIMode)
	if err != nil {
		return skip("invalid_endpoint")
	}
	sample := USTCSpeedSample{TaskID: &task.ID, RoundID: round, AccountID: account.ID, AccountName: account.Name, KeyScope: USTCKeyScope(account), Model: mapped, Source: "probe", Stream: task.Stream, APIMode: task.APIMode, Profile: "probe:" + speedProfileHash(string(body)), Reasoning: "unknown"}
	if task.ThinkingMode == "off" || task.ThinkingMode == "on" {
		sample.Reasoning = task.ThinkingMode
	}
	probe := &ustcSpeedProbeContext{sample: sample, result: make(chan USTCSpeedSample, 1), first: make(chan struct{})}
	probe.guard = func() bool {
		return time.Since(roundStart) <= time.Duration(task.MaxStartSkewSeconds)*time.Second && s.speedTaskCurrent(ctx, task) && s.gateway.speedProbeHasHeadroom(ctx, account, task, true)
	}
	ctx = context.WithValue(ctx, ustcSpeedProbeKey{}, probe)
	ctx = ContextWithUSTCAdmission(ctx, selection)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return skip("invalid_request")
	}
	request.Header.Set("Authorization", "Bearer "+account.GetCredential("api_key"))
	request.Header.Set("Content-Type", "application/json")
	if task.Stream {
		request.Header.Set("Accept", "text/event-stream")
	}
	proxyURL := ""
	if account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	if task.Stream {
		timer = time.AfterFunc(time.Duration(task.FirstOutputTimeoutSeconds)*time.Second, func() {
			select {
			case <-probe.first:
			default:
				cancelCause(errUSTCSpeedFirstTimeout)
			}
		})
	}
	defer timer.Stop()
	response, requestErr := s.gateway.doUSTCUpstream(request, proxyURL, account)
	if response != nil && response.Body != nil {
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
	}
	timer.Stop()
	var result USTCSpeedSample
	select {
	case result = <-probe.result:
	default:
		result = sample
		result.StartedAt = time.Now()
		result.FinishedAt = result.StartedAt
		result.Status = "error"
		result.Reason = "observer_unavailable"
		if requestErr != nil {
			result.Reason = "upstream_network_error"
		}
	}
	if errors.Is(context.Cause(ctx), errUSTCSpeedFirstTimeout) {
		result.Status = "error"
		result.Reason = "first_output_timeout"
	}
	s.persist(result)
	return result.Status
}
