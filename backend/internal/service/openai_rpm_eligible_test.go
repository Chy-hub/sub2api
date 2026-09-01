package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeRPMCache 是 RPMCache 的内存实现，用于 OpenAI 账号级 RPM 调度测试。
type fakeRPMCache struct {
	counts map[int64]int
}

func (f *fakeRPMCache) IncrementRPM(_ context.Context, accountID int64) (int, error) {
	f.counts[accountID]++
	return f.counts[accountID], nil
}

func (f *fakeRPMCache) GetRPM(_ context.Context, accountID int64) (int, error) {
	return f.counts[accountID], nil
}

func (f *fakeRPMCache) GetRPMBatch(_ context.Context, ids []int64) (map[int64]int, error) {
	out := make(map[int64]int, len(ids))
	for _, id := range ids {
		out[id] = f.counts[id]
	}
	return out, nil
}

func TestIsRPMEligible(t *testing.T) {
	tests := []struct {
		name     string
		platform string
		atype    string
		expected bool
	}{
		{"anthropic oauth", PlatformAnthropic, AccountTypeOAuth, true},
		{"anthropic setup-token", PlatformAnthropic, AccountTypeSetupToken, true},
		{"anthropic apikey (not eligible)", PlatformAnthropic, AccountTypeAPIKey, false},
		{"openai apikey", PlatformOpenAI, AccountTypeAPIKey, true},
		{"openai oauth (not eligible)", PlatformOpenAI, AccountTypeOAuth, false},
		{"gemini apikey (not eligible)", PlatformGemini, AccountTypeAPIKey, false},
		{"grok apikey (not eligible)", PlatformGrok, AccountTypeAPIKey, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &Account{Platform: tt.platform, Type: tt.atype}
			require.Equal(t, tt.expected, a.IsRPMEligible())
		})
	}
}

// TestOpenAIRPMSchedulable 覆盖 OpenAI API Key 账号的三态调度判断：
// 绿区放行、黄区非粘性跳过（rpm_sticky_only）、红区不可调度（rpm_exceeded），
// 以及未启用 RPM / 非 RPM 资格账号一律放行、Redis 缺失 fail-open。
func TestOpenAIRPMSchedulable(t *testing.T) {
	newOpenAIAPIKey := func(extra map[string]any) *Account {
		return &Account{
			ID:           1,
			Platform:     PlatformOpenAI,
			Type:         AccountTypeAPIKey,
			Concurrency:  3,
			Extra:        extra,
		}
	}

	t.Run("green zone schedulable", func(t *testing.T) {
		svc := &OpenAIGatewayService{rpmCache: &fakeRPMCache{counts: map[int64]int{1: 10}}}
		acc := newOpenAIAPIKey(map[string]any{"base_rpm": 50})
		ok, reason := svc.rpmSchedulable(context.Background(), acc, false)
		require.True(t, ok)
		require.Empty(t, reason)
	})

	t.Run("yellow zone non-sticky skipped", func(t *testing.T) {
		// base_rpm=15, buffer floor=3 → 黄区 [15, 18)
		svc := &OpenAIGatewayService{rpmCache: &fakeRPMCache{counts: map[int64]int{1: 16}}}
		acc := newOpenAIAPIKey(map[string]any{"base_rpm": 15})
		ok, reason := svc.rpmSchedulable(context.Background(), acc, false)
		require.False(t, ok)
		require.Equal(t, "rpm_sticky_only", reason)
	})

	t.Run("yellow zone sticky allowed", func(t *testing.T) {
		svc := &OpenAIGatewayService{rpmCache: &fakeRPMCache{counts: map[int64]int{1: 16}}}
		acc := newOpenAIAPIKey(map[string]any{"base_rpm": 15})
		ok, reason := svc.rpmSchedulable(context.Background(), acc, true)
		require.True(t, ok)
		require.Empty(t, reason)
	})

	t.Run("red zone not schedulable", func(t *testing.T) {
		// base_rpm=15, buffer floor=3 → 红区 >= 18
		svc := &OpenAIGatewayService{rpmCache: &fakeRPMCache{counts: map[int64]int{1: 20}}}
		acc := newOpenAIAPIKey(map[string]any{"base_rpm": 15})
		ok, reason := svc.rpmSchedulable(context.Background(), acc, false)
		require.False(t, ok)
		require.Equal(t, "rpm_exceeded", reason)
		// 红区下粘性也不可用
		okSticky, _ := svc.rpmSchedulable(context.Background(), acc, true)
		require.False(t, okSticky)
	})

	t.Run("sticky_exempt no red zone", func(t *testing.T) {
		// sticky_exempt 策略无红区，黄区对粘性放行、非粘性跳过
		svc := &OpenAIGatewayService{rpmCache: &fakeRPMCache{counts: map[int64]int{1: 999}}}
		acc := newOpenAIAPIKey(map[string]any{"base_rpm": 15, "rpm_strategy": "sticky_exempt"})
		okSticky, reasonSticky := svc.rpmSchedulable(context.Background(), acc, true)
		require.True(t, okSticky)
		require.Empty(t, reasonSticky)
		okNonSticky, reasonNonSticky := svc.rpmSchedulable(context.Background(), acc, false)
		require.False(t, okNonSticky)
		require.Equal(t, "rpm_sticky_only", reasonNonSticky)
	})

	t.Run("disabled base_rpm 0 always schedulable", func(t *testing.T) {
		svc := &OpenAIGatewayService{rpmCache: &fakeRPMCache{counts: map[int64]int{1: 999}}}
		acc := newOpenAIAPIKey(map[string]any{"base_rpm": 0})
		ok, reason := svc.rpmSchedulable(context.Background(), acc, false)
		require.True(t, ok)
		require.Empty(t, reason)
	})

	t.Run("non-RPM-eligible account always schedulable", func(t *testing.T) {
		// OpenAI OAuth 不在 RPM 范围内，即便有 base_rpm 也不应被拦截
		svc := &OpenAIGatewayService{rpmCache: &fakeRPMCache{counts: map[int64]int{1: 999}}}
		acc := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{"base_rpm": 15}}
		ok, reason := svc.rpmSchedulable(context.Background(), acc, false)
		require.True(t, ok)
		require.Empty(t, reason)
	})

	t.Run("nil rpmCache fail-open", func(t *testing.T) {
		svc := &OpenAIGatewayService{rpmCache: nil}
		acc := newOpenAIAPIKey(map[string]any{"base_rpm": 15})
		ok, reason := svc.rpmSchedulable(context.Background(), acc, false)
		require.True(t, ok)
		require.Empty(t, reason)
	})

	t.Run("uses prefetched context counts", func(t *testing.T) {
		// 预取注入的计数应优先于直接查 Redis，避免热路径逐号查询
		svc := &OpenAIGatewayService{rpmCache: &fakeRPMCache{counts: map[int64]int{1: 0}}}
		acc := newOpenAIAPIKey(map[string]any{"base_rpm": 15})
		ctx := context.WithValue(context.Background(), rpmPrefetchContextKey, map[int64]int{1: 20})
		ok, reason := svc.rpmSchedulable(ctx, acc, false)
		require.False(t, ok)
		require.Equal(t, "rpm_exceeded", reason)
	})
}
