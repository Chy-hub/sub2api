package service

import "context"

// RPMCache RPM 计数器缓存接口
// 用于 Anthropic OAuth/SetupToken 账号的每分钟请求数限制
type RPMCache interface {
	// IncrementRPM 原子递增并返回当前分钟的计数
	// 使用 Redis 服务器时间确定 minute key，避免多实例时钟偏差
	IncrementRPM(ctx context.Context, accountID int64) (count int, err error)

	// GetRPM 获取当前分钟的 RPM 计数
	GetRPM(ctx context.Context, accountID int64) (count int, err error)

	// GetRPMBatch 批量获取多个账号的 RPM 计数（使用 Pipeline）
	GetRPMBatch(ctx context.Context, accountIDs []int64) (map[int64]int, error)
}

// RPMReservationCache optionally supports an atomic, limit-aware RPM reservation.
// The returned cancel function releases an allowed reservation when its request
// was never admitted or sent. Once a request is sent, its reservation remains
// counted even if the upstream request fails.
type RPMReservationCache interface {
	ReserveRPM(ctx context.Context, accountID int64, limit int) (allowed bool, cancel func(), err error)
}
