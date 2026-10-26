package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUSTCSpeedMaxActiveRequestsIncludesProjectedProbe(t *testing.T) {
	for _, tc := range []struct {
		inFlight int
		reserved bool
		allowed  bool
	}{
		{15, false, true},
		{16, false, false},
		{16, true, true},
		{17, true, false},
	} {
		t.Run(fmt.Sprintf("inflight=%d/reserved=%t", tc.inFlight, tc.reserved), func(t *testing.T) {
			_, gateway, _, cache, _, account := newUSTCSpeedRunnerFixture()
			scope := USTCKeyScope(&account)
			for i := 0; i < tc.inFlight; i++ {
				cache.tickets[fmt.Sprint(i)] = &ustcTestTicket{scope: scope, sent: true}
			}
			task := DefaultUSTCSpeedTask()
			task.SkipIfUserWaiting = false
			require.Equal(t, tc.allowed, gateway.speedProbeHasHeadroom(context.Background(), &account, task, tc.reserved))
		})
	}
}

type ustcSpeedTaskRevisionRepo struct {
	USTCSpeedRepository
	task *USTCSpeedTask
}

func (r *ustcSpeedTaskRevisionRepo) GetTask(context.Context, int64) (*USTCSpeedTask, error) {
	if r.task == nil {
		return nil, ErrUSTCSpeedNotFound
	}
	return r.task, nil
}

func TestUSTCSpeedEditedOrDeletedTaskStopsBeforeDispatch(t *testing.T) {
	for _, current := range []*USTCSpeedTask{{ID: 1, Revision: 2}, nil} {
		speed, _, _, _, upstream, account := newUSTCSpeedRunnerFixture()
		speed.repo = &ustcSpeedTaskRevisionRepo{task: current}
		task := DefaultUSTCSpeedTask()
		task.ID, task.Revision = 1, 1
		status, _ := speed.runProbe(context.Background(), task, &account, "deepseek-flash", "prompt", "round", time.Now())
		require.Equal(t, "cancelled", status)
		calls, _ := upstream.snapshot()
		require.Zero(t, calls)
	}
}

func TestUSTCSpeedRunningSnapshotSurvivesEditsButNotDisableOrExpiredLease(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		enabled, expired, want bool
	}{
		{"edited", true, false, true}, {"disabled", false, false, false}, {"expired", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := DefaultUSTCSpeedTask()
			original.ID, original.Revision, original.Enabled = 1, 1, true
			until := time.Now().Add(time.Minute)
			if tc.expired {
				until = time.Now().Add(-time.Second)
			}
			current := original
			current.Revision, current.Enabled, current.LeaseRevision, current.LeaseUntil = 2, tc.enabled, &original.Revision, &until
			svc := NewUSTCSpeedService(&ustcSpeedTaskRevisionRepo{task: &current}, nil, nil)
			require.Equal(t, tc.want, svc.speedTaskCurrent(context.Background(), original))
		})
	}
}

func TestUSTCSpeedCronDoesNotApplyIntervalJitter(t *testing.T) {
	task := DefaultUSTCSpeedTask()
	task.CronExpression, task.Timezone, task.JitterSeconds = "*/1 * * * *", "Asia/Shanghai", 180
	now := time.Date(2026, 10, 26, 5, 4, 30, 0, time.UTC)
	want := time.Date(2026, 10, 26, 5, 5, 0, 0, time.UTC)
	for range 10 {
		require.True(t, want.Equal(nextUSTCSpeedTask(task, now)))
	}
}
