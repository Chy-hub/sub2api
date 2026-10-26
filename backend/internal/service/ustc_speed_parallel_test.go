package service

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

type ustcSpeedParallelEntry struct {
	account int64
	model   string
}

type ustcSpeedParallelUpstream struct {
	*ustcSpeedRunnerUpstream
	entered chan ustcSpeedParallelEntry
	release chan struct{}
	block   string // empty blocks every model
}

func (u *ustcSpeedParallelUpstream) Do(req *http.Request, proxy string, account int64, concurrency int) (*http.Response, error) {
	body, err := req.GetBody()
	if err != nil {
		return nil, err
	}
	var payload struct{ Model string }
	err = json.NewDecoder(body).Decode(&payload)
	_ = body.Close()
	if err != nil {
		return nil, err
	}
	u.entered <- ustcSpeedParallelEntry{account: account, model: payload.Model}
	if u.block == "" || payload.Model == u.block {
		select {
		case <-u.release:
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}
	return u.ustcSpeedRunnerUpstream.Do(req, proxy, account, concurrency)
}

func TestUSTCSpeedIdleParallelismUsesMultipleKeysAndHonorsActualLimit(t *testing.T) {
	speed, _, repo, _, baseUpstream, _ := newUSTCSpeedRunnerFixture()
	gateway, cache := ustcSchedulerFixture(3, false)
	accounts := gateway.accountRepo.(schedulerTestOpenAIAccountRepo)
	for i := range accounts.accounts {
		accounts.accounts[i].Extra[UserInfoQuotaExtraKey("max_parallel_requests")] = 2
	}
	// Duplicate aliases of the same physical Key must not multiply its workers.
	accounts.accounts[2].Credentials["api_key"] = accounts.accounts[0].GetCredential("api_key")
	gateway.accountRepo = accounts
	speed.gateway, speed.accounts, gateway.ustcSpeed = gateway, ustcSpeedAccountsRepo{accounts}, speed
	upstream := &ustcSpeedParallelUpstream{ustcSpeedRunnerUpstream: baseUpstream, entered: make(chan ustcSpeedParallelEntry, 20), release: make(chan struct{})}
	gateway.httpUpstream = upstream
	task := DefaultUSTCSpeedTask()
	task.ID, task.AllAccounts, task.IgnoreTaskConstraints = 1, true, true
	task.Models, task.IdleModelConcurrency = []string{"one", "two", "three", "four", "five"}, 4
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(upstream.release) }) })
	done := make(chan string, 1)
	go func() { status, _ := speed.executeTask(ctx, task, "parallel", true); done <- status }()
	started := map[int64]int{}
	for range 4 {
		select {
		case entry := <-upstream.entered:
			started[entry.account]++
		case <-ctx.Done():
			t.Fatal("models on different Keys did not start concurrently")
		}
	}
	require.Equal(t, map[int64]int{1: 2, 2: 2}, started)
	select {
	case entry := <-upstream.entered:
		t.Fatalf("exceeded the physical Key concurrency: %+v", entry)
	case <-time.After(30 * time.Millisecond):
	}
	release.Do(func() { close(upstream.release) })
	select {
	case status := <-done:
		require.Equal(t, "completed", status)
	case <-ctx.Done():
		t.Fatal("parallel run did not complete")
	}
	samples, acquired, _ := repo.snapshot()
	require.Len(t, samples, 10)
	require.Zero(t, acquired, "idle mode uses the actual shared admission instead of the one-probe background lease")
	for _, sample := range samples {
		require.Equal(t, "success", sample.Status)
	}
	for _, account := range accounts.accounts[:2] {
		capacity, err := cache.USTCRead(ctx, USTCKeyScope(&account), USTCLimits{RPM: 20, Parallel: 2})
		require.NoError(t, err)
		require.Equal(t, 5, capacity.Used)
		require.Zero(t, capacity.InFlight)
		require.Zero(t, capacity.Pending)
	}
}

func TestUSTCSpeedIdleRunContinuesOtherModelsWhileOneIsSlow(t *testing.T) {
	speed, gateway, repo, _, base, _ := newUSTCSpeedRunnerFixture()
	speed.accounts = ustcSpeedAccountsRepo{gateway.accountRepo.(schedulerTestOpenAIAccountRepo)}
	upstream := &ustcSpeedParallelUpstream{ustcSpeedRunnerUpstream: base, entered: make(chan ustcSpeedParallelEntry, 3), release: make(chan struct{}), block: "slow"}
	gateway.httpUpstream = upstream
	task := DefaultUSTCSpeedTask()
	task.Models, task.AllAccounts, task.IgnoreTaskConstraints, task.IdleModelConcurrency = []string{"slow", "fast", "next"}, true, true, 2
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(upstream.release) }) })
	done := make(chan string, 1)
	go func() { status, _ := speed.executeTask(ctx, task, "rolling", true); done <- status }()
	seen := map[string]bool{}
	for range 3 {
		select {
		case entry := <-upstream.entered:
			seen[entry.model] = true
		case <-ctx.Done():
			t.Fatal("a slow model blocked later models")
		}
	}
	require.Equal(t, map[string]bool{"slow": true, "fast": true, "next": true}, seen)
	release.Do(func() { close(upstream.release) })
	require.Equal(t, "completed", <-done)
	samples, _, _ := repo.snapshot()
	require.Len(t, samples, 3)
}

func TestUSTCSpeedIdleProbeWaitsForCapacityWithoutCreatingSkips(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		speed, _, repo, cache, upstream, account := newUSTCSpeedRunnerFixture()
		require.NoError(t, cache.USTCCooldown(context.Background(), USTCKeyScope(&account), 65*time.Second))
		task := DefaultUSTCSpeedTask()
		task.IgnoreTaskConstraints, task.TimeoutSeconds = true, 5
		started := time.Now()
		status, reason := speed.runProbe(context.Background(), task, &account, "model", "prompt", "waiting", started)
		require.Equal(t, "success", status, reason)
		require.GreaterOrEqual(t, time.Since(started), 65*time.Second, "waiting must not consume the five-second request timeout")
		samples, _, _ := repo.snapshot()
		require.Len(t, samples, 1, "admission polling must not create skipped samples")
		require.Equal(t, "success", samples[0].Status)
		calls, _ := upstream.snapshot()
		require.Equal(t, 1, calls, "waiting must not duplicate the model request")
	})
}

func TestUSTCSpeedIdleProbeBoundsCapacityWaiting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		speed, _, repo, cache, upstream, account := newUSTCSpeedRunnerFixture()
		require.NoError(t, cache.USTCCooldown(context.Background(), USTCKeyScope(&account), 10*time.Minute))
		task := DefaultUSTCSpeedTask()
		task.IgnoreTaskConstraints = true
		started := time.Now()
		status, reason := speed.runProbe(context.Background(), task, &account, "model", "prompt", "timeout", started)
		require.Equal(t, "skipped", status)
		require.Equal(t, "capacity_wait_timeout", reason)
		require.Equal(t, ustcSpeedCapacityWait, time.Since(started))
		samples, _, _ := repo.snapshot()
		require.Len(t, samples, 1, "one exhausted wait produces one skipped test item")
		calls, _ := upstream.snapshot()
		require.Zero(t, calls)
	})
}

func TestUSTCSpeedIdleProbeCancelsWhileActualCapacityIsExhausted(t *testing.T) {
	for _, cooldown := range []bool{false, true} {
		t.Run(map[bool]string{false: "RPM exhausted", true: "shared cooldown"}[cooldown], func(t *testing.T) {
			speed, _, repo, cache, upstream, account := newUSTCSpeedRunnerFixture()
			if cooldown {
				require.NoError(t, cache.USTCCooldown(context.Background(), USTCKeyScope(&account), time.Minute))
			} else {
				cache.used[USTCKeyScope(&account)] = 20
			}
			task := DefaultUSTCSpeedTask()
			task.IgnoreTaskConstraints = true
			progress := newUSTCSpeedProgress("cancel")
			ctx, cancel := context.WithCancel(context.WithValue(context.Background(), ustcSpeedProgressKey{}, progress))
			defer cancel()
			done := make(chan string, 1)
			go func() {
				status, _ := speed.runProbe(ctx, task, &account, "model", "prompt", "cancel", time.Now())
				done <- status
			}()
			require.Eventually(t, func() bool {
				active := progress.snapshot().Active
				return len(active) == 1 && active[0].Phase == "waiting_capacity"
			}, time.Second, time.Millisecond)
			cancel()
			select {
			case status := <-done:
				require.Equal(t, "cancelled", status)
			case <-time.After(time.Second):
				t.Fatal("cancellation did not stop capacity waiting")
			}
			calls, _ := upstream.snapshot()
			require.Zero(t, calls, "idle mode cannot exceed actual capacity")
			samples, _, _ := repo.snapshot()
			require.Empty(t, samples, "unsent requests must not create measured attempts")
			require.Empty(t, cache.tickets)
		})
	}
}
