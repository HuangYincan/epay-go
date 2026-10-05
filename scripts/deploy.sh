#!/bin/bash
# epay-go/scripts/deploy.sh

set -e

echo "=== EPay Go 部署脚本 ==="

# 检查环境变量文件
if [ ! -f .env ]; then
    echo "创建 .env 文件..."
    cp .env.example .env
    echo "请编辑 .env 文件配置必要的环境变量"
    exit 1
fi

# 构建镜像
echo "构建 Docker 镜像..."
docker compose build

# 启动服务
echo "启动服务..."

# 后端启动时自动迁移；等待实际健康状态。
echo "等待服务就绪..."
docker compose up -d --wait --wait-timeout 120

echo "=== 部署完成 ==="
echo "前端访问: http://localhost:8081"
echo "后端 API: http://localhost:8080"
