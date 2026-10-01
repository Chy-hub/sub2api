package repository

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// RPM 计数器缓存常量定义
//
// 设计说明：
// 使用 Redis 简单计数器跟踪每个账号每分钟的请求数：
// - Key: rpm:{accountID}:{minuteTimestamp}
// - Value: 当前分钟内的请求计数
// - TTL: 120 秒（覆盖当前分钟 + 一定冗余）
//
// 通过 rdb.Time() 获取服务端时间，避免多实例时钟不同步。普通递增使用
// TxPipeline；限额预留与撤销使用单 key Lua 脚本，兼容 Redis Cluster。
//
// 设计决策：
//   - TxPipeline vs Pipeline：Pipeline 仅合并发送但不保证原子，TxPipeline 使用 MULTI/EXEC 事务保证原子执行。
//   - rdb.Time() 单独调用：脚本执行前需确定当前分钟的 key，因此 TIME 必须单独调用。
//   - 预留脚本只操作该分钟的一个 key，不会触发 Redis Cluster CROSSSLOT。
const (
	// RPM 计数器键前缀
	// 格式: rpm:{accountID}:{minuteTimestamp}
	rpmKeyPrefix = "rpm:"

	// RPM 计数器 TTL（120 秒，覆盖当前分钟窗口 + 冗余）
	rpmKeyTTL = 120 * time.Second

	rpmReservationCancelTimeout = 2 * time.Second
)

var reserveRPMWithLimitScript = redis.NewScript(`
local current = tonumber(redis.call('GET', KEYS[1]) or '0')
local limit = tonumber(ARGV[1])
if limit > 0 and current >= limit then
  return 0
end
redis.call('INCR', KEYS[1])
redis.call('EXPIRE', KEYS[1], tonumber(ARGV[2]))
return 1
`)

var cancelRPMReservationScript = redis.NewScript(`
local current = tonumber(redis.call('GET', KEYS[1]) or '0')
if current <= 0 then
  return 0
end
return redis.call('DECR', KEYS[1])
`)

// RPMCacheImpl RPM 计数器缓存 Redis 实现
type RPMCacheImpl struct {
	rdb *redis.Client
}

// NewRPMCache 创建 RPM 计数器缓存
func NewRPMCache(rdb *redis.Client) service.RPMCache {
	return &RPMCacheImpl{rdb: rdb}
}

var _ service.RPMReservationCache = (*RPMCacheImpl)(nil)

// currentMinuteKey 获取当前分钟的完整 Redis key
// 使用 rdb.Time() 获取 Redis 服务端时间，避免多实例时钟偏差
func (c *RPMCacheImpl) currentMinuteKey(ctx context.Context, accountID int64) (string, error) {
	serverTime, err := c.rdb.Time(ctx).Result()
	if err != nil {
		return "", fmt.Errorf("redis TIME: %w", err)
	}
	minuteTS := serverTime.Unix() / 60
	return fmt.Sprintf("%s%d:%d", rpmKeyPrefix, accountID, minuteTS), nil
}

// currentMinuteSuffix 获取当前分钟时间戳后缀（供批量操作使用）
// 使用 rdb.Time() 获取 Redis 服务端时间
func (c *RPMCacheImpl) currentMinuteSuffix(ctx context.Context) (string, error) {
	serverTime, err := c.rdb.Time(ctx).Result()
	if err != nil {
		return "", fmt.Errorf("redis TIME: %w", err)
	}
	minuteTS := serverTime.Unix() / 60
	return strconv.FormatInt(minuteTS, 10), nil
}

// IncrementRPM 原子递增并返回当前分钟的计数
// 使用 TxPipeline (MULTI/EXEC) 执行 INCR + EXPIRE，保证原子性且兼容 Redis Cluster
func (c *RPMCacheImpl) IncrementRPM(ctx context.Context, accountID int64) (int, error) {
	key, err := c.currentMinuteKey(ctx, accountID)
	if err != nil {
		return 0, fmt.Errorf("rpm increment: %w", err)
	}

	// 使用 TxPipeline (MULTI/EXEC) 保证 INCR + EXPIRE 原子执行
	// EXPIRE 幂等，每次都设置不影响正确性
	pipe := c.rdb.TxPipeline()
	incrCmd := pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, rpmKeyTTL)

	if _, err := pipe.Exec(ctx); err != nil {
		return 0, fmt.Errorf("rpm increment: %w", err)
	}

	return int(incrCmd.Val()), nil
}

// ReserveRPM increments the current minute's counter only when it is below a
// positive limit. A nonpositive limit records the request without limiting it.
func (c *RPMCacheImpl) ReserveRPM(ctx context.Context, accountID int64, limit int) (bool, func(), error) {
	key, err := c.currentMinuteKey(ctx, accountID)
	if err != nil {
		return false, nil, fmt.Errorf("rpm reserve: %w", err)
	}

	allowed, err := reserveRPMWithLimitScript.Run(ctx, c.rdb, []string{key}, limit, int(rpmKeyTTL/time.Second)).Int()
	if err != nil {
		return false, nil, fmt.Errorf("rpm reserve: %w", err)
	}
	if allowed == 0 {
		return false, nil, nil
	}

	var cancelOnce sync.Once
	cancel := func() {
		cancelOnce.Do(func() {
			cancelCtx, cancelContext := context.WithTimeout(context.Background(), rpmReservationCancelTimeout)
			defer cancelContext()
			if err := cancelRPMReservationScript.Run(cancelCtx, c.rdb, []string{key}).Err(); err != nil {
				slog.Warn("openai_rpm_reservation_cancel_failed", "account_id", accountID, "error", err)
			}
		})
	}
	return true, cancel, nil
}

// GetRPM 获取当前分钟的 RPM 计数
func (c *RPMCacheImpl) GetRPM(ctx context.Context, accountID int64) (int, error) {
	key, err := c.currentMinuteKey(ctx, accountID)
	if err != nil {
		return 0, fmt.Errorf("rpm get: %w", err)
	}

	val, err := c.rdb.Get(ctx, key).Int()
	if errors.Is(err, redis.Nil) {
		return 0, nil // 当前分钟无记录
	}
	if err != nil {
		return 0, fmt.Errorf("rpm get: %w", err)
	}
	return val, nil
}

// GetRPMBatch 批量获取多个账号的 RPM 计数（使用 Pipeline）
func (c *RPMCacheImpl) GetRPMBatch(ctx context.Context, accountIDs []int64) (map[int64]int, error) {
	if len(accountIDs) == 0 {
		return map[int64]int{}, nil
	}

	// 获取当前分钟后缀
	minuteSuffix, err := c.currentMinuteSuffix(ctx)
	if err != nil {
		return nil, fmt.Errorf("rpm batch get: %w", err)
	}

	// 使用 Pipeline 批量 GET
	pipe := c.rdb.Pipeline()
	cmds := make(map[int64]*redis.StringCmd, len(accountIDs))
	for _, id := range accountIDs {
		key := fmt.Sprintf("%s%d:%s", rpmKeyPrefix, id, minuteSuffix)
		cmds[id] = pipe.Get(ctx, key)
	}

	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("rpm batch get: %w", err)
	}

	result := make(map[int64]int, len(accountIDs))
	for id, cmd := range cmds {
		if val, err := cmd.Int(); err == nil {
			result[id] = val
		} else {
			result[id] = 0
		}
	}
	return result, nil
}
