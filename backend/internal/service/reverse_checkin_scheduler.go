package service

import (
	"context"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

const reverseCheckinInterval = 10 * time.Minute

// ReverseCheckinScheduler runs the daily check-in for reverse accounts that
// opted in through extra.auto_checkin. Unrecorded failures can retry on later
// ticks; successful claims run once per daily window.
type ReverseCheckinScheduler struct {
	accountRepo AccountRepository
	gateway     *ReverseGateway
	interval    time.Duration
	location    *time.Location
	stopCh      chan struct{}
	stopOnce    sync.Once
	wg          sync.WaitGroup
	ctx         context.Context
	cancel      context.CancelFunc
}

// NewReverseCheckinScheduler builds the scheduler. Start must be called by the
// wire provider.
func NewReverseCheckinScheduler(accountRepo AccountRepository, gateway *ReverseGateway, interval time.Duration) *ReverseCheckinScheduler {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		location = time.FixedZone("CST", 8*3600)
	}
	if interval <= 0 {
		interval = reverseCheckinInterval
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &ReverseCheckinScheduler{
		ctx: ctx, cancel: cancel,
		accountRepo: accountRepo,
		gateway:     gateway,
		interval:    interval,
		location:    location,
		stopCh:      make(chan struct{}),
	}
}

// Start begins the periodic loop.
func (s *ReverseCheckinScheduler) Start() {
	if s == nil || s.accountRepo == nil || s.gateway == nil {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		s.runOnce()
		for {
			select {
			case <-ticker.C:
				s.runOnce()
			case <-s.stopCh:
				return
			}
		}
	}()
}

// Stop terminates the periodic loop.
func (s *ReverseCheckinScheduler) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() { s.cancel(); close(s.stopCh) })
	s.wg.Wait()
}

func (s *ReverseCheckinScheduler) runOnce() {
	if s == nil || s.gateway == nil {
		return
	}
	now := time.Now()
	s.gateway.CleanupLoginSessions(now)
	localNow := now.In(s.location)

	platforms := domain.PlatformIDsWhere(func(spec domain.PlatformSpec) bool {
		return spec.ReverseProvider != ""
	})
	sem := make(chan struct{}, 2)
	var wg sync.WaitGroup
	for _, platform := range platforms {
		accounts, err := s.accountRepo.ListByPlatform(s.ctx, platform)
		if err != nil {
			logger.LegacyPrintf("service.reverse", "reverse checkin list accounts failed: platform=%s err=%v", platform, err)
			continue
		}
		for i := range accounts {
			account := &accounts[i]
			if account.Status != StatusActive || (!reverseAutoCheckinEnabled(account.Extra) && account.Extra[ReverseAutoTasksExtraKey] != true && account.Extra["reverse_auto_refresh"] != true) {
				continue
			}
			doCheckin := reverseAutoCheckinEnabled(account.Extra) && reverseCheckinDue(account.Extra, platform, localNow)
			if !doCheckin && account.Extra[ReverseAutoTasksExtraKey] != true && account.Extra["reverse_auto_refresh"] != true {
				continue
			}
			select {
			case sem <- struct{}{}:
			case <-s.ctx.Done():
				wg.Wait()
				return
			}
			wg.Add(1)
			go func(account *Account, doCheckin bool) {
				defer wg.Done()
				defer func() { <-sem }()
				if doCheckin {
					ctx, cancel := context.WithTimeout(s.ctx, 60*time.Second)
					result, _, err := s.gateway.Checkin(ctx, account)
					cancel()
					if err != nil || result == nil || !result.Success {
						logger.LegacyPrintf("service.reverse", "reverse auto checkin did not succeed: account=%d err=%v", account.ID, err)
					}
				}
				if account.Extra[ReverseAutoTasksExtraKey] == true || account.Extra["reverse_auto_refresh"] == true {
					s.runTasks(account, time.Now().In(s.location))
				}
			}(account, doCheckin)
		}
	}
	wg.Wait()
}

func reverseAutoCheckinEnabled(extra map[string]any) bool {
	if extra == nil {
		return false
	}
	value, ok := extra[ReverseAutoCheckinExtraKey]
	if !ok {
		return false
	}
	enabled, _ := value.(bool)
	return enabled
}

func reverseCheckinDue(extra map[string]any, platform string, now time.Time) bool {
	var windowStart time.Time
	if provider, _, ok := domain.ReverseProviderOf(platform); ok && provider == "qoder" {
		windowStart = time.Date(now.Year(), now.Month(), now.Day(), 10, 5, 0, 0, now.Location())
	} else {
		windowStart = time.Date(now.Year(), now.Month(), now.Day(), 0, 10, 0, 0, now.Location())
	}
	if now.Before(windowStart) {
		return false
	}
	lastAttempt := reverseCheckinLastAttempt(extra)
	if lastAttempt.IsZero() {
		return true
	}
	localLast := lastAttempt.In(now.Location())
	if state, ok := extra[ReverseCheckinExtraKey].(map[string]any); ok {
		details, _ := state["details"].(map[string]any)
		if state["status"] == "failed" && (details == nil || details["not_recorded"] == true) {
			return now.Sub(localLast) >= reverseCheckinInterval
		}
	}
	return localLast.Before(windowStart)
}

func reverseCheckinLastAttempt(extra map[string]any) time.Time {
	if extra == nil {
		return time.Time{}
	}
	raw, ok := extra[ReverseCheckinExtraKey]
	if !ok || raw == nil {
		return time.Time{}
	}
	state, ok := raw.(map[string]any)
	if !ok {
		return time.Time{}
	}
	value, _ := state["last_attempt_at"].(string)
	if value == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}
