package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

type speedOverviewEntry struct {
	expires time.Time
	data    []byte
}
type speedOverviewCache struct {
	mu      sync.Mutex
	entries map[string]speedOverviewEntry
	bytes   int
	flight  singleflight.Group
	once    sync.Once
	slots   chan struct{}
}

func (c *speedOverviewCache) get(key string) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry, ok := c.entries[key]; ok && time.Now().Before(entry.expires) {
		return entry.data
	}
	return nil
}
func (c *speedOverviewCache) put(key string, data []byte) {
	if len(data) > 4<<20 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]speedOverviewEntry)
	}
	for k, e := range c.entries {
		if time.Now().After(e.expires) {
			c.bytes -= len(e.data)
			delete(c.entries, k)
		}
	}
	if previous, ok := c.entries[key]; ok {
		c.bytes -= len(previous.data)
		delete(c.entries, key)
	}
	for len(c.entries) >= 32 || c.bytes+len(data) > 16<<20 {
		var oldest string
		var deadline time.Time
		for k, e := range c.entries {
			if deadline.IsZero() || e.expires.Before(deadline) {
				oldest, deadline = k, e.expires
			}
		}
		c.bytes -= len(c.entries[oldest].data)
		delete(c.entries, oldest)
	}
	c.entries[key] = speedOverviewEntry{expires: time.Now().Add(5 * time.Second), data: data}
	c.bytes += len(data)
}

func (s *USTCSpeedService) cachedOverview(ctx context.Context, q USTCSpeedQuery) (*USTCSpeedOverview, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	keyQuery := q
	keyQuery.AccountIDs = append([]int64(nil), q.AccountIDs...)
	sort.Slice(keyQuery.AccountIDs, func(i, j int) bool { return keyQuery.AccountIDs[i] < keyQuery.AccountIDs[j] })
	// Cache rolling requests briefly while returning the original computed
	// range, never relabelling cached observations as a newer time window.
	if q.LastHours > 0 {
		keyQuery.End = q.End.Truncate(5 * time.Second)
		keyQuery.Start = keyQuery.End.Add(-time.Duration(q.LastHours) * time.Hour)
	}
	var epoch uint64
	if q.UsageFilters == nil {
		epoch = s.overviewEpoch.Load()
	}
	raw, err := json.Marshal(struct {
		Query USTCSpeedQuery
		Epoch uint64
	}{keyQuery, epoch})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	key := hex.EncodeToString(digest[:])
	cache := &s.overviewCache
	decode := func(data []byte) (*USTCSpeedOverview, error) {
		var out USTCSpeedOverview
		err := json.Unmarshal(data, &out)
		return &out, err
	}
	if data := cache.get(key); data != nil {
		return decode(data)
	}
	for attempt := 0; attempt < 2; attempt++ {
		result := cache.flight.DoChan(key, func() (any, error) {
			if data := cache.get(key); data != nil {
				return data, nil
			}
			queryCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			cache.once.Do(func() { cache.slots = make(chan struct{}, 2) })
			select {
			case cache.slots <- struct{}{}:
				defer func() { <-cache.slots }()
			case <-queryCtx.Done():
				return nil, queryCtx.Err()
			}
			out, err := s.repo.Overview(queryCtx, q)
			if err != nil {
				return nil, err
			}
			if out == nil {
				return nil, nil
			}
			out.RequestedSource = q.Source
			out.RangeStart = q.Start
			out.RangeEnd = q.End
			if q.UsageFilters == nil {
				out.DroppedSamples = s.dropped.Load()
			}
			data, err := json.Marshal(out)
			if err != nil {
				return nil, err
			}
			cache.put(key, data)
			return data, nil
		})
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case value := <-result:
			// A cancelled first caller must not cancel another live reader.
			if (errors.Is(value.Err, context.Canceled) || errors.Is(value.Err, context.DeadlineExceeded)) && ctx.Err() == nil && attempt == 0 {
				continue
			}
			if value.Err != nil {
				return nil, value.Err
			}
			if value.Val == nil {
				return nil, nil
			}
			return decode(value.Val.([]byte))
		}
	}
	return nil, context.Canceled
}
