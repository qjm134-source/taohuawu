# Render 部署指南

本文档说明如何将江南水乡智能导游系统部署到 Render 平台。

## 前置条件

1. GitHub 账号
2. Render 账号（免费注册：https://render.com）
3. 智谱 API Key（或其他 LLM API Key）

## 部署步骤

### 1. 准备代码仓库

1. 将代码推送到 GitHub 仓库
2. 确保项目 `deploy/` 目录包含 `render.yaml` 文件

### 2. 在 Render 上创建服务

#### 方式一：使用 Blueprint（推荐）

1. 登录 Render 控制台：https://dashboard.render.com
2. 点击 "New" → "Blueprint"
3. 连接你的 GitHub 仓库
4. Render 会自动识别 `render.yaml` 配置
5. 点击 "Apply" 开始部署

#### 方式二：手动创建服务

1. **创建 PostgreSQL 数据库**
   - 点击 "New" → "PostgreSQL"
   - 名称：`taohuawu-db`
   - 数据库名：`water_town`
   - 用户：`water_town`
   - 计划：Free
   - 点击 "Create Database"

2. **创建后端服务**
   - 点击 "New" → "Web Service"
   - 连接 GitHub 仓库
   - 名称：`taohuawu-backend`
   - 环境：Docker
   - Dockerfile 路径：`./backend/Dockerfile`
   - 计划：Free
   - 添加环境变量：
     - `DATABASE_URL`：从数据库连接信息复制
     - `GLM_API_KEY`：你的智谱 API Key
     - `MIMO_API_KEY`：（可选）
     - `BAILIAN_API_KEY`：（可选）
   - 点击 "Create Web Service"

3. **创建前端服务**
   - 点击 "New" → "Static Site"
   - 连接 GitHub 仓库
   - 名称：`taohuawu-frontend`
   - 构建命令：`echo "Static files ready"`
   - 发布目录：`frontend`
   - 计划：Free
   - 添加环境变量：
     - `BACKEND_URL`：后端服务的 URL（如 `https://taohuawu-backend.onrender.com`）
   - 点击 "Create Static Site"

### 3. 配置环境变量

在 Render 控制台中，为后端服务添加以下环境变量：

| 变量名 | 说明 | 必需 |
|--------|------|------|
| `DATABASE_URL` | PostgreSQL 连接字符串 | ✅ 自动设置 |
| `GLM_API_KEY` | 智谱 API Key | ✅ 必需 |
| `MIMO_API_KEY` | 小米 API Key | ⚪ 可选 |
| `BAILIAN_API_KEY` | 百炼 API Key | ⚪ 可选 |
| `GIN_MODE` | Gin 运行模式 | ✅ 设置为 `release` |

### 4. 更新前端配置

部署完成后，需要更新前端的后端地址：

1. 在 Render 控制台找到后端服务的 URL
2. 在前端服务的环境变量中设置 `BACKEND_URL`
3. 重新部署前端服务

## 验证部署

### 1. 检查服务状态

在 Render 控制台查看服务状态：
- 后端服务：应该显示 "Live"
- 前端服务：应该显示 "Live"
- 数据库：应该显示 "Available"

### 2. 测试后端健康检查

访问后端健康检查端点：
```
https://你的后端地址.onrender.com/health
```

应该返回：
```json
{"status": "ok"}
```

### 3. 测试前端访问

访问前端 URL：
```
https://你的前端地址.onrender.com
```

应该能看到江南水乡导游界面。

## 常见问题

### 1. 数据库连接失败

**问题**：后端日志显示数据库连接错误

**解决方案**：
- 检查 `DATABASE_URL` 环境变量是否正确设置
- 确认数据库服务是否正常运行
- 查看 Render 数据库连接信息

### 2. WebSocket 连接失败

**问题**：前端无法建立 WebSocket 连接

**解决方案**：
- 确认后端服务正在运行
- 检查前端 `BACKEND_URL` 配置是否正确
- 确认使用 `wss://` 协议（HTTPS 环境）

### 3. API Key 无效

**问题**：LLM 调用失败

**解决方案**：
- 检查 API Key 是否正确设置
- 确认 API Key 是否有效
- 查看 API 配额是否充足

### 4. 服务启动慢

**问题**：服务启动时间过长

**解决方案**：
- Render 免费服务启动较慢（可能需要 5-10 分钟）
- 首次部署需要拉取 Docker 镜像，时间较长
- 后续部署会快很多

## 免费额度说明

Render 免费计划包含：
- **750 小时/月** 运行时间（约等于一台服务 24/7 运行一个月）
- **100GB/月** 出站流量
- **1GB** PostgreSQL 存储
- **1个** 免费数据库

超出免费额度后，服务会自动暂停，不会产生额外费用。

## 生产环境建议

如果需要用于生产环境，建议：

1. **升级到付费计划**
   - 更好的性能
   - 更高的可用性
   - 更多的资源

2. **配置自定义域名**
   - 使用自己的域名
   - 配置 SSL 证书

3. **添加监控告警**
   - 设置服务监控
   - 配置告警规则

4. **优化性能**
   - 启用 CDN
   - 优化数据库查询
   - 添加缓存层

## 相关链接

- [Render 官方文档](https://render.com/docs)
- [Render Blueprint 规范](https://render.com/docs/blueprint-spec)
- [Render 免费计划说明](https://render.com/docs/free)
