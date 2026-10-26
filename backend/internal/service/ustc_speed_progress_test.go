package service

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUSTCSpeedProgressConcurrentCountsAndCancellation(t *testing.T) {
	p := newUSTCSpeedProgress("run")
	p.prepare(44, "model")
	var workers sync.WaitGroup
	for i := range 40 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			key := fmt.Sprint(i)
			account := &Account{ID: int64(i), Name: key}
			p.start(key, account, "model", "checking")
			p.start(key, account, "model", "requesting")
			snapshot := p.snapshot()
			if len(snapshot.Active) > 0 {
				snapshot.Active[0].AccountName = "detached copy"
			}
			p.complete(key, []string{"success", "error", "skipped", "cancelled"}[i%4])
		}()
	}
	workers.Wait()
	out := p.snapshot()
	require.Equal(t, 40, out.Completed)
	require.Equal(t, 10, out.Successful)
	require.Equal(t, 10, out.Failed)
	require.Equal(t, 10, out.Skipped)
	require.Equal(t, 10, out.Cancelled)
	require.Empty(t, out.Active)
	p.finish("cancelled", "task_changed")
	out = p.snapshot()
	require.Equal(t, 44, out.Completed)
	require.Equal(t, 14, out.Cancelled, "unstarted later-model test items are cancelled")
	require.Equal(t, "task_changed", out.Reason)
	require.NotNil(t, out.FinishedAt)
	// The optional tracker must not alter direct probe/runner fixtures.
	speedProgress(context.Background()).prepare(1, "model")
}

type ustcSpeedProgressRepo struct {
	*ustcSpeedPolicyRunRepo
	mu     sync.Mutex
	latest *USTCSpeedTaskProgress
}

func (r *ustcSpeedProgressRepo) UpdateTaskProgress(_ context.Context, _ int64, _ string, progress *USTCSpeedTaskProgress) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	copy := *progress
	copy.Active = append([]USTCSpeedActiveProbe(nil), progress.Active...)
	r.latest = &copy
	return nil
}

type ustcSpeedBlockedUpstream struct {
	*ustcSpeedRunnerUpstream
	release chan struct{}
}

func (u *ustcSpeedBlockedUpstream) Do(req *http.Request, proxy string, account int64, concurrency int) (*http.Response, error) {
	select {
	case <-u.release:
		return u.ustcSpeedRunnerUpstream.Do(req, proxy, account, concurrency)
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
}

func TestUSTCSpeedProgressPublishesWhileUpstreamIsRunning(t *testing.T) {
	_, gateway, baseRepo, _, upstream, _ := newUSTCSpeedRunnerFixture()
	blocked := &ustcSpeedBlockedUpstream{ustcSpeedRunnerUpstream: upstream, release: make(chan struct{})}
	gateway.httpUpstream = blocked
	task := DefaultUSTCSpeedTask()
	task.ID, task.Models, task.AllAccounts, task.IgnoreTaskConstraints = 9, []string{"deepseek-flash"}, true, true
	repo := &ustcSpeedProgressRepo{ustcSpeedPolicyRunRepo: &ustcSpeedPolicyRunRepo{ustcSpeedRunnerRepository: baseRepo, task: task, done: make(chan string, 1)}}
	speed := NewUSTCSpeedService(repo, ustcSpeedAccountsRepo{gateway.accountRepo.(schedulerTestOpenAIAccountRepo)}, gateway)
	gateway.ustcSpeed = speed
	t.Cleanup(speed.Stop)
	runID, err := speed.RunTask(context.Background(), task.ID)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		repo.mu.Lock()
		defer repo.mu.Unlock()
		return repo.latest != nil && repo.latest.RunID == runID && repo.latest.Total == 1 && repo.latest.Completed == 0 && len(repo.latest.Active) == 1 && repo.latest.Active[0].Phase == "requesting"
	}, 5*time.Second, 10*time.Millisecond, "heartbeat must expose in-flight work before it completes")
	close(blocked.release)
	select {
	case status := <-repo.done:
		require.Equal(t, "completed", status)
	case <-time.After(3 * time.Second):
		t.Fatal("run did not finish")
	}
	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Equal(t, "completed", repo.latest.State)
	require.Equal(t, 1, repo.latest.Completed)
	require.Equal(t, 1, repo.latest.Successful)
	require.Empty(t, repo.latest.Active)
	require.NotNil(t, repo.latest.FinishedAt)
}
