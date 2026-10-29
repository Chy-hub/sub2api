package service

import (
	"context"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/reverse"
)

func reverseTaskSlot(extra map[string]any, spec reverse.TaskSpec, now time.Time) time.Time {
	var slot time.Time
	if spec.AfterCheckin {
		state, _ := extra[ReverseCheckinExtraKey].(map[string]any)
		last, _ := state["last_success_at"].(string)
		successAt, err := time.Parse(time.RFC3339, last)
		if err != nil || state["status"] != "success" || successAt.In(now.Location()).Format("2006-01-02") != now.Format("2006-01-02") {
			return time.Time{}
		}
		slot = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	} else {
		for _, hour := range spec.DefaultHours {
			candidate := time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, now.Location())
			if !candidate.After(now) && candidate.After(slot) {
				slot = candidate
			}
		}
	}
	return slot
}

func reverseTaskDue(extra map[string]any, spec reverse.TaskSpec, now time.Time) bool {
	slot := reverseTaskSlot(extra, spec, now)
	if slot.IsZero() {
		return false
	}
	schedule, _ := extra[ReverseTaskScheduleExtraKey].(map[string]any)
	last, _ := schedule[spec.Kind].(string)
	lastAt, err := time.Parse(time.RFC3339, last)
	return err != nil || lastAt.Before(slot)
}

func reverseScheduledTaskSpec(extra map[string]any, spec reverse.TaskSpec) (reverse.TaskSpec, bool) {
	refresh := spec.Kind == reverse.TaskKindKeepalive && extra["reverse_auto_refresh"] == true
	if !refresh && extra[ReverseAutoTasksExtraKey] != true {
		return spec, false
	}
	if raw, exists := extra["reverse_task_kinds"]; exists && !refresh {
		allowed := false
		switch kinds := raw.(type) {
		case []string:
			for _, kind := range kinds {
				allowed = allowed || kind == spec.Kind
			}
		case []any:
			for _, kind := range kinds {
				allowed = allowed || kind == spec.Kind
			}
		}
		if !allowed {
			return spec, false
		}
	}
	if hours, ok := extra["reverse_task_hours"].(map[string]any); ok {
		if raw, exists := hours[spec.Kind]; exists {
			parsed, err := reverseTaskHours(raw)
			if err != nil {
				return spec, false
			}
			spec.DefaultHours = parsed
		}
	}
	return spec, true
}

func (g *ReverseGateway) runScheduledTask(ctx context.Context, account *Account, spec reverse.TaskSpec, now time.Time) error {
	unlock, err := g.lockOperation(ctx, account.ID)
	if err != nil {
		return err
	}
	defer unlock()
	// Re-read after taking the lock: another operation may have completed, or
	// an administrator may have disabled automation since this tick began.
	if g.accountRepo != nil {
		account, err = g.accountRepo.GetByID(ctx, account.ID)
		if err != nil {
			return err
		}
	}
	if account == nil || account.Status != StatusActive || ctx.Err() != nil {
		return ctx.Err()
	}
	spec, enabled := reverseScheduledTaskSpec(account.Extra, spec)
	if !enabled || !reverseTaskDue(account.Extra, spec, now) {
		return nil
	}
	schedule, _ := account.Extra[ReverseTaskScheduleExtraKey].(map[string]any)
	updated := make(map[string]any, len(schedule)+1)
	for key, value := range schedule {
		updated[key] = value
	}
	// Reserve the actual slot, rather than the completion timestamp. A task
	// finishing in the next slot must not suppress that slot's execution.
	updated[spec.Kind] = reverseTaskSlot(account.Extra, spec, now).UTC().Format(time.RFC3339)
	if err := g.storeReverseExtra(ctx, account, ReverseTaskScheduleExtraKey, updated); err != nil {
		return err
	}
	_, err = g.runTask(ctx, account, ReverseTaskRequest{Action: "scheduled", Kind: spec.Kind})
	return err
}

func (s *ReverseCheckinScheduler) runTasks(account *Account, now time.Time) {
	var specs []reverse.TaskSpec
	if account.Extra[ReverseAutoTasksExtraKey] == true {
		if p, acc, err := s.gateway.taskProvider(account); err == nil {
			specs = reverseTaskSpecs(p, acc)
		}
	}
	if account.Extra["reverse_auto_refresh"] == true {
		found := false
		for _, spec := range specs {
			if spec.Kind == reverse.TaskKindKeepalive {
				found = true
			}
		}
		if !found {
			specs = append(specs, reverse.TaskSpec{Kind: reverse.TaskKindKeepalive, DefaultHours: []int{22}})
		}
	}
	for _, spec := range specs {
		if s.ctx.Err() != nil {
			return
		}
		ctx, cancel := context.WithTimeout(s.ctx, 10*time.Minute)
		err := s.gateway.runScheduledTask(ctx, account, spec, now)
		cancel()
		if errors.Is(err, ErrReverseOperationBusy) {
			continue
		}
		if err != nil {
			logger.LegacyPrintf("service.reverse", "reverse scheduled task failed: account=%d kind=%s err=%v", account.ID, spec.Kind, err)
		}
	}
}
