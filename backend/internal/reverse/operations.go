package reverse

import (
	"context"
	"time"
)

// Quota and task contracts belong to the account management layer. They never
// replace the gateway's token accounting or the user's configured spending limits.
type QuotaSubscription struct {
	Plan     string `json:"plan,omitempty"`
	TierName string `json:"tierName,omitempty"`
	TierID   string `json:"tierId,omitempty"`
}

func ClampFraction(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

type QuotaMetric struct {
	Key      string  `json:"key"`
	Label    string  `json:"label"`
	Value    float64 `json:"value"`
	Unit     string  `json:"unit,omitempty"`
	Format   string  `json:"format,omitempty"`
	Currency string  `json:"currency,omitempty"`
}

type QuotaBucket struct {
	Window            string  `json:"window,omitempty"`
	RemainingFraction float64 `json:"remainingFraction"`
	ResetTime         string  `json:"resetTime,omitempty"`
	Description       string  `json:"description,omitempty"`
}

type QuotaGroup struct {
	DisplayName string        `json:"displayName,omitempty"`
	Buckets     []QuotaBucket `json:"buckets,omitempty"`
}

type QuotaFetchResponse struct {
	Subscription *QuotaSubscription `json:"subscription,omitempty"`
	Summary      []QuotaMetric      `json:"summary,omitempty"`
	Groups       []QuotaGroup       `json:"groups,omitempty"`
}

type QuotaProvider interface {
	Quota(context.Context, *Account) (*QuotaFetchResponse, error)
}

type TaskSpec struct {
	Kind         string `json:"kind"`
	Label        string `json:"label"`
	Description  string `json:"description,omitempty"`
	DefaultHours []int  `json:"default_hours,omitempty"`
	AfterCheckin bool   `json:"after_checkin,omitempty"`
	Manual       bool   `json:"manual,omitempty"`
	UsesChat     bool   `json:"uses_chat,omitempty"`
	Route        string `json:"route,omitempty"`
}

const TaskKindKeepalive = "keepalive"

type TaskItem struct {
	Kind     string `json:"kind"`
	Code     string `json:"code"`
	RunCode  string `json:"run_code,omitempty"`
	Title    string `json:"title"`
	Current  int64  `json:"current"`
	Target   int64  `json:"target"`
	UsesChat bool   `json:"uses_chat"`
}

type TaskInspection struct {
	Items    []TaskItem `json:"items"`
	InPeriod bool       `json:"in_period"`
}

type TaskRunResult struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	Credit  int64  `json:"credit"`
	Energy  int64  `json:"energy"`
}

type AutoTaskResult struct {
	Code     string `json:"code"`
	Status   string `json:"status"`
	Message  string `json:"message"`
	Credit   int64  `json:"credit"`
	Energy   int64  `json:"energy"`
	Progress string `json:"progress,omitempty"`
}

type TaskStatusView struct {
	Code     string `json:"code"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Progress int64  `json:"progress"`
	Target   int64  `json:"target"`
}

type TaskStatusResult struct {
	InPeriod bool             `json:"in_period"`
	Tasks    []TaskStatusView `json:"tasks"`
	Chances  any              `json:"chances,omitempty"`
	Error    string           `json:"error,omitempty"`
}

type TaskProvider interface {
	TaskSpecs(*Account) []TaskSpec
	InspectTasks(context.Context, *Account) (TaskInspection, error)
	RunTask(context.Context, *Account, string) (TaskRunResult, error)
	RunAllTasks(context.Context, *Account) []AutoTaskResult
	RunScheduled(context.Context, *Account, string) (string, error)
	TaskStatus(context.Context, *Account) TaskStatusResult
}

// SleepContext preserves the reference client's pacing and stops promptly when
// an administrator cancels a task or the application shuts down.
func SleepContext(ctx context.Context, wait time.Duration) bool {
	if ctx.Err() != nil {
		return false
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func FirstNonZero(values ...int64) int64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}
