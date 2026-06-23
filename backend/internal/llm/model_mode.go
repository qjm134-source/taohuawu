package llm

import (
	"sync"
	"time"
)

// ModelMode 模型工作模式
type ModelMode string

const (
	ModeStream ModelMode = "stream" // 流式模式
	ModeChat   ModelMode = "chat"   // 非流式模式
	ModeAuto   ModelMode = "auto"   // 自动检测模式
)

// ModelModeRecord 模型模式记录
type ModelModeRecord struct {
	Mode         ModelMode  // 当前使用的模式
	LastSuccess  time.Time  // 最后成功时间
	LastFailure  time.Time  // 最后失败时间
	FailureCount int        // 连续失败次数
	SuccessCount int        // 连续成功次数
}

// ModelModeRegistry 模型模式注册表（记忆每个模型的成功模式）
type ModelModeRegistry struct {
	records map[string]*ModelModeRecord
	mu      sync.RWMutex
}

// 全局模型模式注册表
var globalModeRegistry = &ModelModeRegistry{
	records: make(map[string]*ModelModeRecord),
}

// GetModeRegistry 获取全局模式注册表
func GetModeRegistry() *ModelModeRegistry {
	return globalModeRegistry
}

// GetModelMode 获取模型的工作模式
// 如果配置为 stream 或 chat，直接返回
// 如果配置为 auto，返回记忆的成功模式（默认先尝试 stream）
func (r *ModelModeRegistry) GetModelMode(modelName string, configMode string) ModelMode {
	// 如果配置明确指定了模式，直接使用配置的模式
	if configMode == "stream" {
		return ModeStream
	}
	if configMode == "chat" {
		return ModeChat
	}

	// auto 模式：查找记忆的成功模式
	r.mu.RLock()
	record, exists := r.records[modelName]
	r.mu.RUnlock()

	if !exists {
		// 第一次使用，默认先尝试流式
		return ModeStream
	}

	// 如果最近成功次数较多，继续使用成功的模式
	if record.SuccessCount >= 3 {
		return record.Mode
	}

	// 如果最近失败次数较多，切换到另一种模式
	if record.FailureCount >= 2 {
		if record.Mode == ModeStream {
			return ModeChat
		}
		return ModeStream
	}

	// 默认使用记录的模式
	return record.Mode
}

// RecordSuccess 记录模型成功
func (r *ModelModeRegistry) RecordSuccess(modelName string, mode ModelMode) {
	r.mu.Lock()
	defer r.mu.Unlock()

	record, exists := r.records[modelName]
	if !exists {
		record = &ModelModeRecord{
			Mode: mode,
		}
		r.records[modelName] = record
	}

	// 如果模式改变，重置计数
	if record.Mode != mode {
		record.Mode = mode
		record.SuccessCount = 0
		record.FailureCount = 0
	}

	record.LastSuccess = time.Now()
	record.SuccessCount++
	record.FailureCount = 0 // 成功后重置失败计数
}

// RecordFailure 记录模型失败
func (r *ModelModeRegistry) RecordFailure(modelName string, mode ModelMode) {
	r.mu.Lock()
	defer r.mu.Unlock()

	record, exists := r.records[modelName]
	if !exists {
		record = &ModelModeRecord{
			Mode: mode,
		}
		r.records[modelName] = record
	}

	record.LastFailure = time.Now()
	record.FailureCount++
	record.SuccessCount = 0 // 失败后重置成功计数
}

// GetRecord 获取模型的模式记录
func (r *ModelModeRegistry) GetRecord(modelName string) *ModelModeRecord {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if record, exists := r.records[modelName]; exists {
		// 返回副本，避免外部修改
		return &ModelModeRecord{
			Mode:         record.Mode,
			LastSuccess:  record.LastSuccess,
			LastFailure:  record.LastFailure,
			FailureCount: record.FailureCount,
			SuccessCount: record.SuccessCount,
		}
	}
	return nil
}

// GetAllRecords 获取所有模型的模式记录
func (r *ModelModeRegistry) GetAllRecords() map[string]*ModelModeRecord {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// 返回副本
	result := make(map[string]*ModelModeRecord)
	for name, record := range r.records {
		result[name] = &ModelModeRecord{
			Mode:         record.Mode,
			LastSuccess:  record.LastSuccess,
			LastFailure:  record.LastFailure,
			FailureCount: record.FailureCount,
			SuccessCount: record.SuccessCount,
		}
	}
	return result
}

// Clear 清空所有记录
func (r *ModelModeRegistry) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = make(map[string]*ModelModeRecord)
}