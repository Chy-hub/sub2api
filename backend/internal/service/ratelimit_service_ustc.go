package service

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const (
	ustcTransient429Cooldown  = time.Minute
	ustcBudgetRecheckCooldown = 30 * time.Second
)

type ustcRateLimitExtendingRepository interface {
	SetRateLimitedIfLater(ctx context.Context, id int64, resetAt time.Time) error
}

// USTC is OpenAI-compatible at the protocol layer, but its 429 responses do
// not use Codex's long usage windows. A usable Retry-After is authoritative;
// otherwise explicit budget errors get a short recheck cooldown and the quota
// gate decides whether the account can rejoin the pool.
func (s *RateLimitService) handleUSTC429(ctx context.Context, account *Account, headers http.Header, responseBody []byte) {
	if s == nil || account == nil || account.ID <= 0 {
		return
	}

	now := time.Now()
	var resetAt time.Time
	reason := "ustc_429_transient"
	if isUSTCBudgetExhaustion429(responseBody) {
		// A known reset is a precise reason to revisit the quota snapshot, not a
		// cooldown to persist for its full (possibly multi-hour) duration. Recheck
		// at the nearest exhausted-window reset or in 30 seconds, whichever comes
		// first; the quota eligibility gate keeps the account out if still spent.
		resetAt = now.Add(ustcBudgetRecheckCooldown)
		if nextReset, ok := ustcNearestExhaustedBudgetReset(account, now); ok && nextReset.Before(resetAt) {
			resetAt = nextReset
		}
		reason = "ustc_429_budget_recheck"
		if delay := retryAfter(headers, now); delay > 0 {
			resetAt = now.Add(delay)
		}
	} else {
		resetAt = now.Add(ustc429Delay(headers, now))
		reason = "ustc_429_sync_wait"
		if s.ustcCapacityCache != nil {
			if err := s.ustcCapacityCache.USTCCooldown(ctx, USTCKeyScope(account), time.Until(resetAt)); err != nil {
				slog.Warn("ustc_shared_cooldown_failed", "account_id", account.ID, "error", err)
			}
		}
	}
	if s.ustcCapacityNotify != nil {
		s.ustcCapacityNotify()
	}

	s.notifyAccountSchedulingBlocked(account, resetAt, reason)
	if s.accountRepo == nil {
		slog.Warn("ustc_429_set_rate_limited_skipped", "account_id", account.ID, "reason", "account repository is not configured")
		return
	}
	var err error
	if extender, ok := s.accountRepo.(ustcRateLimitExtendingRepository); ok {
		err = extender.SetRateLimitedIfLater(ctx, account.ID, resetAt)
	} else {
		err = s.accountRepo.SetRateLimited(ctx, account.ID, resetAt)
	}
	if err != nil {
		slog.Warn("ustc_429_set_rate_limited_failed", "account_id", account.ID, "error", err)
		return
	}
	slog.Info("ustc_account_rate_limited", "account_id", account.ID, "reset_at", resetAt, "reason", reason)
}

// ustcNearestExhaustedBudgetReset returns the nearest future reset reported by
// a currently exhausted window. It is only a recheck hint; the quota eligibility
// gate determines whether the account is usable when it is selected again.
func ustcNearestExhaustedBudgetReset(account *Account, now time.Time) (time.Time, bool) {
	if account == nil || len(account.Extra) == 0 {
		return time.Time{}, false
	}

	var nearest time.Time
	for _, window := range userInfoQuotaWindowsForScheduling(account.Extra) {
		if !userInfoQuotaWindowExhausted(window) {
			continue
		}
		parsed, err := parseUserInfoTime(window.ResetAt)
		if err != nil || !parsed.After(now) {
			continue
		}
		if nearest.IsZero() || parsed.Before(nearest) {
			nearest = parsed
		}
	}
	return nearest, !nearest.IsZero()
}

// isUSTCBudgetExhaustion429 is intentionally narrow. Generic quota and rate
// limit wording often describes RPM; only explicit budget/spend-limit language
// qualifies as a budget denial without corroborating snapshot data.
func isUSTCBudgetExhaustion429(responseBody []byte) bool {
	text := strings.ToLower(strings.TrimSpace(string(responseBody)))
	if text == "" {
		return false
	}
	text = strings.NewReplacer("_", " ", "-", " ").Replace(text)
	for _, phrase := range []string{
		"budget exceeded", "budget has been exceeded", "budget limit exceeded", "budget limit has been exceeded",
		"budget exhausted", "budget is exhausted", "budget has been exhausted", "budget depleted", "budget has been depleted",
		"no remaining budget", "remaining budget is zero", "budget limit reached", "budget has been reached",
		"spend limit exceeded", "spend limit reached", "spending limit exceeded", "spending limit reached",
		"预算超", "预算耗尽", "预算已耗尽", "预算用完", "预算已用完", "预算不足", "预算已不足",
		"消费限额超", "消费限额耗尽", "消费限额用完", "消费限额不足",
	} {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}
