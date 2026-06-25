-- MySQL 数据库初始化脚本
-- GORM 会自动创建表结构，这里只是作为备份参考

-- 玩家表
CREATE TABLE IF NOT EXISTS players (
    id VARCHAR(64) PRIMARY KEY,
    tenant_id VARCHAR(32) NOT NULL,
    nickname VARCHAR(64) NOT NULL,
    device_id VARCHAR(128),
    first_visit_time TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_visit_time TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    total_dialogues INT NOT NULL DEFAULT 0,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_players_tenant ON players(tenant_id);
CREATE INDEX idx_players_device ON players(device_id);

-- 对话记录表
CREATE TABLE IF NOT EXISTS conversations (
    id VARCHAR(64) PRIMARY KEY,
    player_id VARCHAR(64) NOT NULL,
    tenant_id VARCHAR(32) NOT NULL,
    session_id VARCHAR(64) NOT NULL,
    user_message TEXT NOT NULL,
    ai_message TEXT NOT NULL,
    emotion VARCHAR(16),
    tools_used JSON,
    llm_model VARCHAR(32),
    llm_tokens INT,
    cost DECIMAL(10,6),
    cache_hit BOOLEAN DEFAULT FALSE,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_conversations_player ON conversations(player_id);
CREATE INDEX idx_conversations_session ON conversations(session_id);
CREATE INDEX idx_conversations_tenant ON conversations(tenant_id);
CREATE INDEX idx_conversations_created ON conversations(created_at);

-- 审计日志表
CREATE TABLE IF NOT EXISTS audit_logs (
    id VARCHAR(64) PRIMARY KEY,
    player_id VARCHAR(64),
    tenant_id VARCHAR(32) NOT NULL,
    action VARCHAR(32) NOT NULL,
    request_payload JSON,
    response_payload JSON,
    error_message TEXT,
    status VARCHAR(16) NOT NULL,
    latency_ms INT,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_audit_tenant ON audit_logs(tenant_id);
CREATE INDEX idx_audit_action ON audit_logs(action);
CREATE INDEX idx_audit_created ON audit_logs(created_at);

-- 插入测试数据（可选）
-- INSERT INTO players (id, tenant_id, nickname, device_id, total_dialogues)
-- VALUES ('test_player_001', 'tenant_001', '测试玩家', 'test_device', 0);
