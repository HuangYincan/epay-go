# EPay Go 部署指南

## 环境要求

- Docker 20.10+
- Docker Compose 2.0+
- 2GB+ 内存
- 10GB+ 磁盘空间

## 快速开始

### 1. 克隆项目

```bash
git clone https://github.com/HuangYincan/epay-go.git
cd epay-go
```

### 2. 配置环境变量

```bash
cp .env.example .env
# 编辑 .env 文件，配置必要的环境变量
```

### 3. 启动服务

```bash
# 本机访问，或使用宿主机 Nginx / Cloudflare Tunnel 反代
docker compose up -d --build

# Caddy 提供公网 HTTPS；仅适用于宿主机 80/443 未被占用
docker compose -f docker-compose.prod.caddy.yml up -d --build
```

### 4. 访问服务

- 默认 Compose 前端: http://127.0.0.1:8081
- 默认 Compose 后端 API: http://127.0.0.1:8080
- 默认 Compose 管理后台: http://127.0.0.1:8081/admin/login
- 默认 Compose 商户中心: http://127.0.0.1:8081/merchant/login

默认 Compose 的 PostgreSQL 和 Redis 不发布宿主机端口，前端和后端只监听宿主机回环。公网域名需反代到前端 `127.0.0.1:8081`，或参考 `deploy/nginx/host.prod.conf.example` 分别转发到前后端。Caddy 方案仅发布 80/443。支付回调路径需允许支付渠道访问。

## 常用命令

```bash
# 查看日志
docker-compose logs -f

# 查看服务状态
docker-compose ps

# 停止服务
docker-compose down

# 重新构建
docker-compose build --no-cache

# 进入容器
docker-compose exec backend sh
docker-compose exec postgres psql -U epay
```

## 数据备份

```bash
# 备份数据库
docker-compose exec postgres pg_dump -U epay epay > backup.sql

# 恢复数据库
cat backup.sql | docker-compose exec -T postgres psql -U epay epay
```

## 更新部署

```bash
# 拉取最新代码
git pull

# 重新构建并启动
docker-compose build
docker-compose up -d
```

## 故障排查

### 后端无法连接数据库

检查 PostgreSQL 是否就绪：
```bash
docker-compose logs postgres
```

### 前端无法访问后端 API

检查 Nginx 代理配置和后端服务状态：
```bash
docker-compose logs frontend
docker-compose logs backend
```
