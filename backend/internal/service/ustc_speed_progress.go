package service

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"
)

type ustcSpeedProgressKey struct{}

type ustcSpeedProgressTracker struct {
	mu     sync.Mutex
	value  USTCSpeedTaskProgress
	active map[string]USTCSpeedActiveProbe
}

func newUSTCSpeedProgress(runID string) *ustcSpeedProgressTracker {
	now := time.Now()
	return &ustcSpeedProgressTracker{
		value:  USTCSpeedTaskProgress{RunID: runID, State: "preparing", StartedAt: now, UpdatedAt: now},
		active: make(map[string]USTCSpeedActiveProbe),
	}
}

func speedProgress(ctx context.Context) *ustcSpeedProgressTracker {
	progress, _ := ctx.Value(ustcSpeedProgressKey{}).(*ustcSpeedProgressTracker)
	return progress
}

func (p *ustcSpeedProgressTracker) prepare(total int, model string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.value.Total, p.value.CurrentModel, p.value.State = total, model, "running"
}

func (p *ustcSpeedProgressTracker) start(key string, account *Account, model, phase string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	active, exists := p.active[key]
	if !exists {
		active = USTCSpeedActiveProbe{AccountID: account.ID, AccountName: account.Name, Model: model, StartedAt: time.Now()}
	}
	active.Phase = phase
	p.active[key] = active
}

func (p *ustcSpeedProgressTracker) complete(key, status string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.active, key)
	p.value.Completed++
	switch status {
	case "success":
		p.value.Successful++
	case "skipped":
		p.value.Skipped++
	case "cancelled":
		p.value.Cancelled++
	default:
		p.value.Failed++
	}
}

func (p *ustcSpeedProgressTracker) finish(state, reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	p.value.State, p.value.Reason, p.value.FinishedAt = state, reason, &now
	// Future models and workers cancelled before admission have no upstream
	// sample. Count these unfinished test items as cancelled, never successful.
	if remaining := p.value.Total - p.value.Completed; remaining > 0 {
		p.value.Cancelled += remaining
		p.value.Completed += remaining
	}
	clear(p.active)
}

func (p *ustcSpeedProgressTracker) snapshot() USTCSpeedTaskProgress {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := p.value
	out.UpdatedAt = time.Now()
	out.Active = make([]USTCSpeedActiveProbe, 0, len(p.active))
	for _, active := range p.active {
		out.Active = append(out.Active, active)
	}
	sort.Slice(out.Active, func(i, j int) bool { return out.Active[i].AccountID < out.Active[j].AccountID })
	return out
}

func (s *USTCSpeedService) publishProgress(id int64, owner string, progress *ustcSpeedProgressTracker) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	snapshot := progress.snapshot()
	if err := s.repo.UpdateTaskProgress(ctx, id, owner, &snapshot); err != nil {
		slog.Warn("ustc_speed_progress_write_failed", "task_id", id, "error", err)
	}
}

func (s *USTCSpeedService) progressHeartbeat(ctx context.Context, id int64, owner string, progress *ustcSpeedProgressTracker, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	s.publishProgress(id, owner, progress)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.publishProgress(id, owner, progress)
		}
	}
}
