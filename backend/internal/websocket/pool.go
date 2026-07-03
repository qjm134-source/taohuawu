package websocket

import (
	"context"
	"sync"

	"github.com/watertown/guide/pkg/utils"
)

// WorkerPool 工作池（按租户隔离）
type WorkerPool struct {
	pools map[string]chan struct{}
	mu    sync.RWMutex
}

// NewWorkerPool 创建工作池
func NewWorkerPool() *WorkerPool {
	return &WorkerPool{
		pools: make(map[string]chan struct{}),
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

// Acquire 获取资源
func (p *WorkerPool) Acquire(tenantID string, size int) context.CancelFunc {
	pool := p.GetPool(tenantID, size)
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		defer utils.RecoverWithLog("WorkerPool")
		pool <- struct{}{}
		<-ctx.Done()
		<-pool
	}()

	return cancel
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
