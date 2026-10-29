package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/reverse"
)

var ErrReverseOperationBusy = errors.New("another operation is running for this account")

const (
	ReverseQuotaExtraKey        = "reverse_quota"
	ReverseTasksExtraKey        = "reverse_tasks"
	ReverseTaskScheduleExtraKey = "reverse_task_schedule"
	ReverseAutoTasksExtraKey    = "reverse_auto_tasks"
)

func (g *ReverseGateway) lockOperation(ctx context.Context, id int64) (func(), error) {
	value, _ := g.operationLocks.LoadOrStore(id, make(chan struct{}, 1))
	ch := value.(chan struct{})
	select {
	case ch <- struct{}{}:
	default:
		return nil, ErrReverseOperationBusy
	}
	lockCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// Share the project's Redis / PostgreSQL coordination so separate service
	// instances cannot execute account tasks or check-in concurrently.
	release, acquired := tryAcquireSingletonLeaderLock(lockCtx, g.lockCache, g.lockDB, fmt.Sprintf("reverse:operation:%d", id), reverseRandomID(), 11*time.Minute)
	if !acquired {
		<-ch
		return nil, ErrReverseOperationBusy
	}
	return func() { release(); <-ch }, nil
}

func (g *ReverseGateway) storeReverseExtra(ctx context.Context, account *Account, key string, value any) error {
	if g.accountRepo != nil {
		if err := g.accountRepo.UpdateExtra(ctx, account.ID, map[string]any{key: value}); err != nil {
			return err
		}
	}
	if account.Extra == nil {
		account.Extra = map[string]any{}
	}
	account.Extra[key] = value
	return nil
}

// Quota queries the upstream balance; gateway spending limits stay independent.
func (g *ReverseGateway) Quota(ctx context.Context, account *Account) (*reverse.QuotaFetchResponse, error) {
	provider, acc, err := g.providerFor(account)
	if err != nil {
		return nil, err
	}
	quotaProvider, ok := provider.(reverse.QuotaProvider)
	if !ok {
		return nil, fmt.Errorf("upstream quota is not supported")
	}
	callCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	result, err := quotaProvider.Quota(callCtx, acc)
	var upstream *reverse.UpstreamError
	if errors.As(err, &upstream) && upstream.StatusCode == 401 && g.refreshAccount(callCtx, account) == nil {
		_, acc, _ = g.providerFor(account)
		result, err = quotaProvider.Quota(callCtx, acc)
	}
	if err != nil {
		return nil, err
	}
	if err = g.storeReverseExtra(ctx, account, ReverseQuotaExtraKey, map[string]any{"updated_at": time.Now().UTC().Format(time.RFC3339), "quota": result}); err != nil {
		return nil, err
	}
	return result, nil
}

type ReverseTasksOverview struct {
	Specs      []reverse.TaskSpec       `json:"specs"`
	Inspection reverse.TaskInspection   `json:"inspection"`
	Status     reverse.TaskStatusResult `json:"status"`
	LastRun    any                      `json:"last_run,omitempty"`
}

func (g *ReverseGateway) taskProvider(account *Account) (reverse.TaskProvider, *reverse.Account, error) {
	p, acc, err := g.providerFor(account)
	if err != nil {
		return nil, nil, err
	}
	tasks, ok := p.(reverse.TaskProvider)
	if !ok {
		return nil, nil, fmt.Errorf("this provider has no growth tasks")
	}
	return tasks, acc, nil
}

func reverseTaskSpecs(p reverse.TaskProvider, acc *reverse.Account) []reverse.TaskSpec {
	specs := append([]reverse.TaskSpec(nil), p.TaskSpecs(acc)...)
	if acc.Region == "cn" {
		specs = append(specs, reverse.TaskSpec{Kind: "growth", Label: "成长任务", Description: "按依赖顺序完成全部可执行成长任务", DefaultHours: []int{9, 21}, Manual: true, UsesChat: true})
	}
	for i := range specs {
		if specs[i].Kind == "school" || specs[i].Kind == "blackcat" {
			specs[i].UsesChat = true
		}
	}
	return specs
}

func (g *ReverseGateway) TaskOverview(ctx context.Context, account *Account) (*ReverseTasksOverview, error) {
	p, acc, err := g.taskProvider(account)
	if err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	inspection, err := p.InspectTasks(callCtx, acc)
	if err != nil {
		return nil, err
	}
	return &ReverseTasksOverview{Specs: reverseTaskSpecs(p, acc), Inspection: inspection, Status: p.TaskStatus(callCtx, acc), LastRun: account.Extra[ReverseTasksExtraKey]}, nil
}

type ReverseTaskRequest struct {
	Action string `json:"action" binding:"required,oneof=run all scheduled"`
	Code   string `json:"code"`
	Kind   string `json:"kind"`
}

type ReverseTaskOutcome struct {
	StartedAt  string                   `json:"started_at"`
	FinishedAt string                   `json:"finished_at"`
	Success    bool                     `json:"success"`
	Action     string                   `json:"action"`
	Kind       string                   `json:"kind,omitempty"`
	Results    []reverse.AutoTaskResult `json:"results"`
	Message    string                   `json:"message,omitempty"`
}

func (g *ReverseGateway) RunTask(ctx context.Context, account *Account, request ReverseTaskRequest) (*ReverseTaskOutcome, error) {
	unlock, err := g.lockOperation(ctx, account.ID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	return g.runTask(ctx, account, request)
}

// runTask requires the account operation lock. Scheduled calls reserve their
// slot under the same lock before any upstream request can have side effects.
func (g *ReverseGateway) runTask(ctx context.Context, account *Account, request ReverseTaskRequest) (*ReverseTaskOutcome, error) {
	p, acc, err := g.taskProvider(account)
	if err != nil && !(request.Action == "scheduled" && request.Kind == reverse.TaskKindKeepalive) {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	result := &ReverseTaskOutcome{StartedAt: time.Now().UTC().Format(time.RFC3339), Success: true, Action: request.Action, Kind: request.Kind}
	switch request.Action {
	case "all":
		result.Results = p.RunAllTasks(callCtx, acc)
	case "run":
		if strings.TrimSpace(request.Code) == "" {
			return nil, fmt.Errorf("task code is required")
		}
		run, runErr := p.RunTask(callCtx, acc, request.Code)
		err = runErr
		result.Results = []reverse.AutoTaskResult{{Code: request.Code, Status: run.Status, Message: run.Message, Credit: run.Credit, Energy: run.Energy}}
	case "scheduled":
		valid := request.Kind == reverse.TaskKindKeepalive
		var specs []reverse.TaskSpec
		if p != nil {
			specs = reverseTaskSpecs(p, acc)
		}
		for _, spec := range specs {
			if spec.Kind == request.Kind {
				valid = true
				break
			}
		}
		if !valid {
			return nil, fmt.Errorf("unsupported task kind")
		}
		if request.Kind == reverse.TaskKindKeepalive {
			_, err = g.Refresh(callCtx, account)
			if err == nil {
				result.Message = "Token refreshed"
			}
		} else if request.Kind == "growth" {
			result.Results = p.RunAllTasks(callCtx, acc)
		} else {
			result.Message, err = p.RunScheduled(callCtx, acc, request.Kind)
		}
	default:
		return nil, fmt.Errorf("unsupported task action")
	}
	for _, item := range result.Results {
		if item.Status == "error" || item.Status == "failed" || item.Status == "pending" {
			result.Success = false
		}
	}
	if callCtx.Err() != nil {
		err = callCtx.Err()
	}
	if err != nil {
		result.Success = false
		if result.Message == "" {
			result.Message = err.Error()
		} else {
			result.Message += "; " + err.Error()
		}
		if len(result.Results) == 1 && result.Results[0].Status == "" {
			result.Results[0].Status = "error"
			result.Results[0].Message = err.Error()
		}
	}
	result.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	// Persist the outcome even when the request was canceled midway through a
	// workflow; it prevents the scheduler from replaying unknown side effects.
	storeCtx, storeCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer storeCancel()
	if storeErr := g.storeReverseExtra(storeCtx, account, ReverseTasksExtraKey, result); storeErr != nil {
		result.Success = false
		result.Message = strings.TrimSpace(result.Message + "; failed to save task result: " + storeErr.Error())
		return result, storeErr
	}
	return result, err
}
