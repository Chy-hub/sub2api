package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type ustcLeaseBatchRecorder struct {
	*ustcTestCapacityCache
	batches chan []*USTCTicket
}

func (c *ustcLeaseBatchRecorder) USTCRenewBatch(_ context.Context, tickets []*USTCTicket) error {
	c.batches <- tickets
	return nil
}

func TestUSTCLeaseRenewerBatchesActiveTicketsAndStopsWhenIdle(t *testing.T) {
	_, base := ustcSchedulerFixture(1, false)
	cache := &ustcLeaseBatchRecorder{ustcTestCapacityCache: base, batches: make(chan []*USTCTicket, 10)}
	renewer := &ustcLeaseRenewer{cache: cache, tickets: make(map[*USTCTicket]struct{}), wake: make(chan struct{}, 1), interval: 20 * time.Millisecond}
	a, b := &USTCTicket{Scope: "key-a", ID: "1"}, &USTCTicket{Scope: "key-b", ID: "2"}
	renewer.register(a)
	renewer.register(b)
	select {
	case tickets := <-cache.batches:
		require.ElementsMatch(t, []*USTCTicket{a, b}, tickets)
	case <-time.After(time.Second):
		t.Fatal("active leases were not renewed together")
	}
	renewer.unregister(a)
	select {
	case tickets := <-cache.batches:
		require.Equal(t, []*USTCTicket{b}, tickets)
	case <-time.After(time.Second):
		t.Fatal("remaining lease was not renewed")
	}
	renewer.unregister(b)
	require.Eventually(t, func() bool { renewer.mu.Lock(); defer renewer.mu.Unlock(); return !renewer.running }, time.Second, time.Millisecond)
	// Registering after the worker exits must restart the same manager.
	renewer.register(a)
	defer renewer.unregister(a)
	select {
	case tickets := <-cache.batches:
		require.Equal(t, []*USTCTicket{a}, tickets)
	case <-time.After(time.Second):
		t.Fatal("idle worker did not restart")
	}
}

func TestUSTCSelectionReleaseCombinesPendingAndSlotCleanupOnce(t *testing.T) {
	svc, cache := ustcSchedulerFixture(1, false)
	account := svc.accountRepo.(schedulerTestOpenAIAccountRepo).accounts[0]
	reservation, _, err := svc.reserveUSTC(context.Background(), &account)
	require.NoError(t, err)
	selection := &AccountSelectionResult{Account: &account, ustcAdmission: reservation}
	released := 0
	release := selection.ReleaseWithUSTCAdmission(func() { released++ })
	release()
	release()
	require.Equal(t, 1, released)
	capacity, err := cache.USTCRead(context.Background(), USTCKeyScope(&account), USTCLimits{20, 20})
	require.NoError(t, err)
	require.Zero(t, capacity.Pending)
	require.Zero(t, capacity.InFlight)
}
