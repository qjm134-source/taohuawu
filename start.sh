#!/bin/bash
# 桃花坞智能导游系统 - 一键启动脚本

echo "=========================================="
echo "  桃花坞智能导游系统 - Docker 一键部署"
echo "=========================================="

# 检查 Docker 是否运行
if ! docker info &> /dev/null; then
    echo "❌ Docker 未运行，请先启动 Docker Desktop"
    exit 1
fi

# 检查是否设置了 GLM_API_KEY
if [ -z "$GLM_API_KEY" ]; then
    echo "⚠️  警告：未设置 GLM_API_KEY 环境变量"
    echo "   如果需要调用 GLM 模型，请先执行：export GLM_API_KEY=your_api_key"
fi

echo ""
echo "📦 正在启动服务..."
echo ""

# 启动所有服务
docker-compose up -d

echo ""
echo "✅ 服务启动完成！"
echo ""
echo "=========================================="
echo " 服务访问地址："
echo "=========================================="
echo " 🌐 前端页面:        http://localhost:3000"
echo " 🚀 后端 API:         http://localhost:8080"
echo " 📊 Prometheus:       http://localhost:9090"
echo " 🔍 Jaeger UI:        http://localhost:16686"
echo " 🛢️  MySQL:           localhost:3306"
echo "=========================================="
echo ""
echo "📝 查看日志: docker-compose logs -f"
echo "⏹️  停止服务: docker-compose down"