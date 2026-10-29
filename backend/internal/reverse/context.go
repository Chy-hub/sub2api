package reverse

import (
	"context"
	"net"
)

type contextKey uint8

const (
	clientIPKey contextKey = iota
	loginAccountKey
)

func WithClientIP(ctx context.Context, ip string) context.Context {
	if parsed := net.ParseIP(ip); parsed != nil {
		return context.WithValue(ctx, clientIPKey, parsed.String())
	}
	return ctx
}
func ClientIP(ctx context.Context) string { value, _ := ctx.Value(clientIPKey).(string); return value }
func WithLoginAccount(ctx context.Context, acc *Account) context.Context {
	return context.WithValue(ctx, loginAccountKey, acc)
}
func LoginAccount(ctx context.Context) *Account {
	acc, _ := ctx.Value(loginAccountKey).(*Account)
	return acc
}
func LoginProxyURL(ctx context.Context) string {
	if acc := LoginAccount(ctx); acc != nil {
		return acc.ProxyURL
	}
	return ""
}
