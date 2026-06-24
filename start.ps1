<#
桃花坞智能导游系统 - Windows 一键启动脚本
#>

Write-Host "=========================================="
Write-Host "  桃花坞智能导游系统 - Docker 一键部署"
Write-Host "=========================================="

# 检查 Docker 是否运行
try {
    docker info | Out-Null
} catch {
    Write-Host "❌ Docker 未运行，请先启动 Docker Desktop" -ForegroundColor Red
    exit 1
}

Write-Host ""
Write-Host "📦 正在启动服务..."
Write-Host ""

# 启动所有服务
docker-compose up -d

Write-Host ""
Write-Host "✅ 服务启动完成！" -ForegroundColor Green
Write-Host ""
Write-Host "=========================================="
Write-Host " 服务访问地址："
Write-Host "=========================================="
Write-Host " 🌐 前端页面:        http://localhost:3000"
Write-Host " 🚀 后端 API:         http://localhost:8080"
Write-Host " 📊 Prometheus:       http://localhost:9090"
Write-Host " 🔍 Jaeger UI:        http://localhost:16686"
Write-Host " 🛢️  MySQL:           localhost:3306"
Write-Host "=========================================="
Write-Host ""
Write-Host "📝 查看日志: docker-compose logs -f"
Write-Host "⏹️  停止服务: docker-compose down"