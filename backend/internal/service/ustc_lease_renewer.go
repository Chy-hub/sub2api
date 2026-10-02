package service

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

type ustcCapacityBatchRenewer interface {
	USTCRenewBatch(context.Context, []*USTCTicket) error
}

// One worker per gateway renews all active upstream leases. Repository caches
// pipeline the lightweight renew scripts instead of reading the capacity state.
type ustcLeaseRenewer struct {
	mu       sync.Mutex
	cache    USTCCapacityCache
	tickets  map[*USTCTicket]struct{}
	running  bool
	wake     chan struct{}
	interval time.Duration
}

func (s *OpenAIGatewayService) ustcLeaseRenewer(cache USTCCapacityCache) *ustcLeaseRenewer {
	s.ustcLeaseOnce.Do(func() {
		s.ustcLeases = &ustcLeaseRenewer{cache: cache, tickets: make(map[*USTCTicket]struct{}), wake: make(chan struct{}, 1), interval: 30 * time.Second}
	})
	return s.ustcLeases
}

func (r *ustcLeaseRenewer) register(ticket *USTCTicket) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tickets[ticket] = struct{}{}
	if !r.running {
		r.running = true
		go r.run()
	}
}

func (r *ustcLeaseRenewer) unregister(ticket *USTCTicket) {
	r.mu.Lock()
	delete(r.tickets, ticket)
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *ustcLeaseRenewer) run() {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		r.mu.Lock()
		if len(r.tickets) == 0 {
			r.running = false
			r.mu.Unlock()
			return
		}
		r.mu.Unlock()
		select {
		case <-r.wake:
			continue
		case <-ticker.C:
		}
		r.mu.Lock()
		tickets := make([]*USTCTicket, 0, len(r.tickets))
		for ticket := range r.tickets {
			tickets = append(tickets, ticket)
		}
		r.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if batch, ok := r.cache.(ustcCapacityBatchRenewer); ok {
			if err := batch.USTCRenewBatch(ctx, tickets); err != nil {
				slog.Warn("ustc_parallel_lease_renew_failed", "error", err)
			}
		} else {
			for _, ticket := range tickets {
				if err := r.cache.USTCRenew(ctx, ticket); err != nil {
					slog.Warn("ustc_parallel_lease_renew_failed", "error", err)
				}
			}
		}
		cancel()
	}
}
