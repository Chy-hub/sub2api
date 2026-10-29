package service

import (
	"github.com/Wei-Shaw/sub2api/internal/reverse"
	"github.com/Wei-Shaw/sub2api/internal/reverse/qoder"
	"github.com/Wei-Shaw/sub2api/internal/reverse/workbuddy"
)

// registerReverseProviders wires the built-in reverse adapters.
func registerReverseProviders(registry *reverse.Registry) {
	registry.Register("qoder", qoder.New())
	registry.Register("workbuddy", workbuddy.New())
}
