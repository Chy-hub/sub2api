package service

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"
)

func speedIdleModelConcurrency(task USTCSpeedTask) int {
	parallel := task.IdleModelConcurrency
	if parallel == 0 {
		parallel = DefaultUSTCSpeedTask().IdleModelConcurrency
	}
	return min(20, max(1, parallel))
}

// Each physical Key has its own queue. Finishing one model immediately frees a
// worker for the next, without a barrier on other models or accounts. Shared
// admission remains authoritative across these workers and real user traffic.
func (s *USTCSpeedService) executeConcurrentTask(ctx context.Context, task USTCSpeedTask, owner string, accounts []Account) (string, string) {
	progress := speedProgress(ctx)
	progress.prepare(len(accounts)*len(task.Models), "")
	prompts := make([]string, len(task.Models))
	for i := range prompts {
		prompts[i] = task.Prompt
		if len(task.PromptVariants) > 0 {
			prompts[i] = task.PromptVariants[rand.IntN(len(task.PromptVariants))]
		}
	}
	results := make(chan ustcProbeResult, len(accounts)*len(task.Models))
	var workers sync.WaitGroup
	for _, candidate := range accounts {
		account := candidate
		parallel := min(speedIdleModelConcurrency(task), len(task.Models))
		if limits, known := USTCAccountLimits(&account); known && limits.Parallel > 0 {
			parallel = min(parallel, limits.Parallel)
		}
		jobs := make(chan int, len(task.Models))
		for i := range task.Models {
			jobs <- i
		}
		close(jobs)
		for range parallel {
			workers.Add(1)
			// Model mapping has a lazy cache on Account. Give each worker its
			// own account value while sharing the immutable credential maps.
			go func(account Account) {
				defer workers.Done()
				for i := range jobs {
					if !s.speedTaskCurrent(ctx, task) {
						results <- ustcProbeResult{status: "cancelled", reason: "task_changed"}
						return
					}
					round := fmt.Sprintf("%s-%d", owner, i)
					key := fmt.Sprintf("%s:%d", round, account.ID)
					progress.start(key, &account, task.Models[i], "checking")
					status, reason := s.runProbe(ctx, task, &account, task.Models[i], prompts[i], round, time.Now())
					progress.complete(key, status)
					results <- ustcProbeResult{status: status, reason: reason}
				}
			}(account)
		}
	}
	workers.Wait()
	close(results)
	successes, failures, skipped := 0, 0, 0
	reasons := map[string]int{}
	cancelled := false
	for result := range results {
		if result.reason != "" && result.status != "success" {
			reasons[result.reason]++
		}
		switch result.status {
		case "success":
			successes++
		case "skipped":
			skipped++
		case "cancelled":
			cancelled = true
		default:
			failures++
		}
	}
	if ctx.Err() != nil || cancelled {
		return "cancelled", "cancelled"
	}
	detail := summarizeSpeedReasons(reasons)
	slog.Info("ustc_speed_parallel_run", "task_id", task.ID, "models_per_key", speedIdleModelConcurrency(task), "success", successes, "skipped", skipped, "failed", failures)
	if failures > 0 {
		if successes > 0 {
			return "partial", detail
		}
		return "failed", detail
	}
	if successes == 0 && skipped > 0 {
		return "skipped", detail
	}
	return "completed", detail
}
