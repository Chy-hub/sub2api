package service

import (
	"context"
	"errors"
	"time"
)

type ustcCapacityReadsKey struct{}
type ustcCapacityReads map[string]USTCCapacity

type ustcPoolJob struct {
	ctx                    context.Context
	groupID                *int64
	accounts               []Account
	sessionHash, model     string
	excluded               map[int64]struct{}
	compact, preferLowRate bool
	capability             OpenAIEndpointCapability
	deadline               time.Time
	lastError              error
	done                   bool
	result                 chan ustcPoolResult
}
type ustcPoolResult struct {
	selection *AccountSelectionResult
	err       error
}
type ustcPoolCoordinator struct {
	service *OpenAIGatewayService
	state   *ustcAccountPoolState
	scope   string
	wake    chan struct{}
	jobs    []*ustcPoolJob // protected by state.mu; dispatched in admission order
}

func (s *OpenAIGatewayService) notifyUSTCCapacity() {
	state := s.defaultUSTCPoolState()
	state.mu.Lock()
	defer state.mu.Unlock()
	for _, coordinator := range state.coordinators {
		select {
		case coordinator.wake <- struct{}{}:
		default:
		}
	}
}

func (s *OpenAIGatewayService) selectBalancedDefaultUSTCAccountWithWait(ctx context.Context, groupID *int64, accounts []Account, sessionHash, model string, excluded map[int64]struct{}, compact bool, capability OpenAIEndpointCapability, preferLowRate bool) (*AccountSelectionResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cfg := s.schedulingConfig()
	maximum := cfg.FallbackMaxWaiting
	if maximum <= 0 {
		maximum = 1
	}
	allowed, release := s.acquireDefaultUSTCPoolWait(ctx, groupID, maximum, maxUSTCDuration(time.Second, cfg.FallbackWaitTimeout)+2*time.Second)
	if !allowed {
		return nil, &ustcPoolCapacityError{cause: noAvailableOpenAISelectionError(model, false, "ustc_pool_wait_full"), retryAfter: time.Second}
	}
	if release != nil {
		defer release()
	}
	job := &ustcPoolJob{ctx: ctx, groupID: groupID, accounts: cloneUSTCPool(accounts), sessionHash: sessionHash, model: model, excluded: cloneExcludedAccountIDs(excluded), compact: compact, capability: capability, preferLowRate: preferLowRate, result: make(chan ustcPoolResult, 1)}
	state := s.defaultUSTCPoolState()
	scope, _, _ := s.defaultUSTCPoolScope(groupID)
	state.mu.Lock()
	coordinator := state.coordinators[scope]
	if coordinator == nil {
		coordinator = &ustcPoolCoordinator{service: s, state: state, scope: scope, wake: make(chan struct{}, 1)}
		state.coordinators[scope] = coordinator
		go coordinator.run()
	}
	coordinator.jobs = append(coordinator.jobs, job)
	state.mu.Unlock()
	select {
	case coordinator.wake <- struct{}{}:
	default:
	}
	select {
	case result := <-job.result:
		return result.selection, result.err
	case <-ctx.Done():
		// The coordinator owns any concurrent dispatch and releases an unclaimed slot.
		select {
		case coordinator.wake <- struct{}{}:
		default:
		}
		go func() {
			result := <-job.result
			if result.selection != nil && result.selection.ReleaseFunc != nil {
				result.selection.ReleaseFunc()
			}
		}()
		return nil, ctx.Err()
	}
}

func (c *ustcPoolCoordinator) run() {
	for {
		c.state.mu.Lock()
		jobs := append([]*ustcPoolJob(nil), c.jobs...)
		c.state.mu.Unlock()
		if len(jobs) == 0 {
			timer := time.NewTimer(5 * time.Second)
			select {
			case <-c.wake:
				timer.Stop()
				continue
			case <-timer.C:
			}
			c.state.mu.Lock()
			if len(c.jobs) == 0 {
				delete(c.state.coordinators, c.scope)
				c.state.mu.Unlock()
				return
			}
			c.state.mu.Unlock()
			continue
		}
		// One set of shared reads for a dispatch pass, regardless of waiter count.
		reads := make(ustcCapacityReads)
		pause := time.Hour
		for _, job := range jobs {
			finish := func(selection *AccountSelectionResult, err error) {
				job.done = true
				job.result <- ustcPoolResult{selection: selection, err: err}
			}
			if err := job.ctx.Err(); err != nil {
				finish(nil, err)
				continue
			}
			if !job.deadline.IsZero() && !time.Now().Before(job.deadline) {
				finish(nil, job.lastError)
				continue
			}
			ctx := context.WithValue(job.ctx, ustcCapacityReadsKey{}, reads)
			job.accounts = c.service.supplementDefaultUSTCPool(ctx, job.groupID, job.accounts)
			selection, err := c.service.selectBalancedDefaultUSTCAccount(ctx, job.groupID, job.accounts, job.sessionHash, job.model, job.excluded, job.compact, job.capability, job.preferLowRate)
			var capacity *ustcPoolCapacityError
			if !errors.As(err, &capacity) {
				finish(selection, err)
				continue
			}
			job.lastError = err
			cfg := c.service.schedulingConfig()
			if job.deadline.IsZero() {
				job.deadline = time.Now().Add(cfg.FallbackWaitTimeout)
				if deadline, ok := job.ctx.Deadline(); ok && deadline.Before(job.deadline) {
					job.deadline = deadline
				}
			}
			remaining := time.Until(job.deadline)
			if cfg.FallbackWaitTimeout <= 0 || cfg.FallbackMaxWaiting <= 0 || remaining <= 0 || (!capacity.mutable && capacity.retryAfter > remaining) {
				finish(nil, err)
				continue
			}
			delay := capacity.retryAfter
			// Remote instance releases/metadata have no local event; one pool-level
			// fallback check covers all waiters. Known immutable RPM waits use a timer.
			if capacity.mutable {
				delay = min(delay, time.Second)
			}
			pause = min(pause, maxUSTCDuration(time.Millisecond, min(delay, remaining)))
		}
		c.state.mu.Lock()
		pending := c.jobs[:0]
		for _, job := range c.jobs {
			if !job.done {
				pending = append(pending, job)
			}
		}
		c.jobs = pending
		if len(c.jobs) > len(jobs) {
			pause = time.Millisecond
		}
		c.state.mu.Unlock()
		if pause == time.Hour {
			continue
		}
		timer := time.NewTimer(pause)
		select {
		case <-c.wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func readUSTCCapacity(ctx context.Context, cache USTCCapacityCache, scope string, limits USTCLimits) (USTCCapacity, error) {
	reads, _ := ctx.Value(ustcCapacityReadsKey{}).(ustcCapacityReads)
	if value, ok := reads[scope]; ok {
		return value, nil
	}
	value, err := cache.USTCRead(ctx, scope, limits)
	if err == nil && reads != nil {
		reads[scope] = value
	}
	return value, err
}
