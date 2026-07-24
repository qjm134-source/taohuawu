package websocket

import (
	"context"
	"sync"

	"github.com/watertown/guide/pkg/logging"
)

// WorkerPool 工作池（按租户隔离）
type WorkerPool struct {
	pools  map[string]chan struct{}
	mu     sync.RWMutex
	logger logging.Logger
}

// NewWorkerPool 创建工作池。
func NewWorkerPool(logger logging.Logger) *WorkerPool {
	return &WorkerPool{
		pools:  make(map[string]chan struct{}),
		logger: logger,
	}
}

// GetPool 获取租户的工作池
func (p *WorkerPool) GetPool(tenantID string, size int) chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()

	if pool, exists := p.pools[tenantID]; exists {
		return pool
	}

	pool := make(chan struct{}, size)
	p.pools[tenantID] = pool
	return pool
}

// Acquire 获取资源（同步方式），返回释放函数。
// 如果 ctx 在获取资源前取消，返回空函数。
func (p *WorkerPool) Acquire(ctx context.Context, tenantID string, size int) func() {
	pool := p.GetPool(tenantID, size)

	select {
	case pool <- struct{}{}:
		return func() { <-pool }
	case <-ctx.Done():
		return func() {}
	}
}

// GetSize 获取租户工作池大小
func (p *WorkerPool) GetSize(tenantID string) int {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if pool, exists := p.pools[tenantID]; exists {
		return cap(pool)
	}
	return 0
}

// RemovePool 移除租户工作池
func (p *WorkerPool) RemovePool(tenantID string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if pool, exists := p.pools[tenantID]; exists {
		close(pool)
		delete(p.pools, tenantID)
	}
}
