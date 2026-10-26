package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type speedCacheRepo struct {
	USTCSpeedRepository
	calls atomic.Int32
	query func(context.Context, USTCSpeedQuery) (*USTCSpeedOverview, error)
}

func (r *speedCacheRepo) Overview(ctx context.Context, q USTCSpeedQuery) (*USTCSpeedOverview, error) {
	r.calls.Add(1)
	return r.query(ctx, q)
}

func speedCacheQuery() USTCSpeedQuery {
	end := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	return USTCSpeedQuery{Start: end.Add(-24 * time.Hour), End: end, Source: "probe", Metric: "overall_tps", AccountIDs: []int64{2, 1}}
}

func TestUSTCSpeedCacheIsDetachedAndSeparatesQueriesAndNewSamples(t *testing.T) {
	repo := &speedCacheRepo{query: func(context.Context, USTCSpeedQuery) (*USTCSpeedOverview, error) {
		return &USTCSpeedOverview{Series: []USTCSpeedPoint{{Model: "original"}}}, nil
	}}
	svc := NewUSTCSpeedService(repo, nil, nil)
	q := speedCacheQuery()
	out, err := svc.Overview(context.Background(), q)
	require.NoError(t, err)
	out.Series[0].Model = "mutated by reader"
	q.AccountIDs = []int64{1, 2}
	out, err = svc.Overview(context.Background(), q)
	require.NoError(t, err)
	require.Equal(t, "original", out.Series[0].Model)
	require.EqualValues(t, 1, repo.calls.Load(), "equivalent account filters share the cache")
	q.Model = "different"
	_, err = svc.Overview(context.Background(), q)
	require.NoError(t, err)
	require.EqualValues(t, 2, repo.calls.Load())
	svc.overviewEpoch.Add(1)
	_, err = svc.Overview(context.Background(), q)
	require.NoError(t, err)
	require.EqualValues(t, 3, repo.calls.Load(), "new probes invalidate cached benchmark data")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = svc.Overview(ctx, q)
	require.ErrorIs(t, err, context.Canceled, "even cache hits honor caller cancellation")
}

func TestUSTCSpeedCacheBoundsParallelQueriesAndCoalescesReaders(t *testing.T) {
	var active, maximum atomic.Int32
	started := make(chan struct{}, 10)
	release := make(chan struct{})
	repo := &speedCacheRepo{query: func(ctx context.Context, _ USTCSpeedQuery) (*USTCSpeedOverview, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := maximum.Load(); n > old && !maximum.CompareAndSwap(old, n); old = maximum.Load() {
		}
		started <- struct{}{}
		select {
		case <-release:
			return &USTCSpeedOverview{}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	svc := NewUSTCSpeedService(repo, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	results := make(chan error, 10)
	for i := range 10 {
		q := speedCacheQuery()
		q.Model = string(rune('a' + i%5))
		go func() { _, err := svc.Overview(ctx, q); results <- err }()
	}
	for range 2 {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("parallel queries did not start")
		}
	}
	require.EqualValues(t, 2, maximum.Load())
	close(release)
	for range 10 {
		require.NoError(t, <-results)
	}
	require.EqualValues(t, 5, repo.calls.Load(), "duplicate readers run one query")
	require.EqualValues(t, 2, maximum.Load(), "distinct queries share two database slots")
}

func TestUSTCSpeedCacheCancelledOwnerDoesNotCancelLiveFollower(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			started := make(chan struct{}, 1)
			release := make(chan struct{})
			var first atomic.Bool
			repo := &speedCacheRepo{query: func(ctx context.Context, _ USTCSpeedQuery) (*USTCSpeedOverview, error) {
				if first.CompareAndSwap(false, true) {
					started <- struct{}{}
					select {
					case <-release:
						return nil, cause
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
				return &USTCSpeedOverview{}, nil
			}}
			svc := NewUSTCSpeedService(repo, nil, nil)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			result := make(chan error, 2)
			go func() { _, err := svc.Overview(ctx, speedCacheQuery()); result <- err }()
			<-started
			go func() { _, err := svc.Overview(ctx, speedCacheQuery()); result <- err }()
			close(release)
			for range 2 {
				require.NoError(t, <-result)
			}
			require.EqualValues(t, 2, repo.calls.Load())
		})
	}
}

func TestUSTCSpeedCacheDoesNotRetainFailuresOrOversizedEntries(t *testing.T) {
	want := errors.New("database unavailable")
	repo := &speedCacheRepo{query: func(context.Context, USTCSpeedQuery) (*USTCSpeedOverview, error) { return nil, want }}
	svc := NewUSTCSpeedService(repo, nil, nil)
	for range 2 {
		_, err := svc.Overview(context.Background(), speedCacheQuery())
		require.ErrorIs(t, err, want)
	}
	require.EqualValues(t, 2, repo.calls.Load())
	var cache speedOverviewCache
	cache.put("expired", []byte("old"))
	cache.entries["expired"] = speedOverviewEntry{expires: time.Now().Add(-time.Second), data: []byte("old")}
	require.Nil(t, cache.get("expired"))
	for i := range 40 {
		cache.put(string(rune('a'+i)), make([]byte, 1<<20))
	}
	require.LessOrEqual(t, cache.bytes, 16<<20)
	require.LessOrEqual(t, len(cache.entries), 32)
	cache.put("too large", make([]byte, (4<<20)+1))
	require.Nil(t, cache.get("too large"))
}
