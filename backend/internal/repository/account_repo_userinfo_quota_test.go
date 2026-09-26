package repository

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// upstream_userinfo_* 是纯观测额度快照（无调度消费方），必须保持中性，
// 否则每次探测都会走事务 + outbox + 平台桶重建。
func TestUserInfoQuotaExtraIsSchedulerNeutral(t *testing.T) {
	for _, key := range []string{
		"upstream_userinfo_max_budget",
		"upstream_userinfo_spend",
		"upstream_userinfo_remaining",
		"upstream_userinfo_valid",
		"upstream_userinfo_expires_at",
		"upstream_userinfo_budget_reset_at",
		"upstream_userinfo_key_alias",
		"upstream_userinfo_windows",
		"upstream_userinfo_updated_at",
	} {
		require.Truef(t, isSchedulerNeutralExtraKey(key), "key %q must be scheduler-neutral", key)
	}
	require.False(t, shouldEnqueueSchedulerOutboxForExtraUpdates(map[string]any{
		"upstream_userinfo_max_budget": 100.0,
		"upstream_userinfo_windows":    []map[string]any{{"duration": "3h"}},
	}))
}
