# EPay Go 部署指南

## 环境要求

- Docker 20.10+
- 支持 `up --wait` 的 Docker Compose v2
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
# 编辑 .env：配置数据库和 Redis 密码、至少 32 字节的随机 JWT_SECRET
# 首次启动填写至少 12 字符的 DEFAULT_ADMIN_PASSWORD
```

### 3. 启动服务

```bash
# 本机访问，或使用宿主机 Nginx / Cloudflare Tunnel 反代
docker compose up -d --build

# Caddy 提供公网 HTTPS；仅适用于宿主机 80/443 未被占用
docker compose -f docker compose.prod.caddy.yml up -d --build
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
docker compose logs -f

# 查看服务状态
docker compose ps

# 停止服务
docker compose down

# 重新构建
docker compose build --no-cache

# 进入容器
docker compose exec backend sh
docker compose exec postgres psql -U epay
```

## 数据备份

```bash
# 备份数据库
docker compose exec postgres pg_dump -U epay epay > backup.sql

# 恢复数据库
cat backup.sql | docker compose exec -T postgres psql -U epay epay
```

## 更新部署

```bash
# 拉取最新代码
git pull

# 重新构建并启动
docker compose build
docker compose up -d
```

## 已有站点升级核对

1. 暂停下单、退款审批及结算，备份数据库并验证备份能恢复。迁移前停止旧版本后端，避免新旧账务代码同时写库。
2. 更换曾使用示例值的 JWT 密钥和管理员密码。如果旧版商户订单接口曾被访问，支付通道私钥和 APIv3 密钥可能已泄露，应到支付服务商轮换，再更新后台配置。
3. 核对费率单位：新版本 `0.6` 表示 `0.6%`。若旧配置用小数比例 `0.006` 表示 `0.6%`，先改为 `0.6`。已创建订单的手续费不会被重新计算，历史错误手续费和余额需对账调整。
4. 检查同商户重复订单号，包括软删除订单：

   ```sql
   SELECT merchant_id, out_trade_no, COUNT(*)
   FROM orders
   GROUP BY merchant_id, out_trade_no
   HAVING COUNT(*) > 1;
   ```

   有重复时迁移明确报错。逐笔核对服务商交易记录，保留真实支付、账务与通知关联，再为重复历史记录设置不会再次使用的独立商户订单号。不要直接删除已支付订单或合并余额。
5. 旧版可能把处理中退款记为成功，或将部分退款订单标为全额退款；还可能留下重复冻结/扣款。应按服务商退款单号核对历史 `refunds`、`settlements`、`balance_records` 和商户余额。新版本不会推测并自动改写这些历史账务。确认部分退款成功且累计小于订单金额后，才将对应订单恢复为已支付；确认真实退款结果后再修复历史退款状态与余额。
6. 构建新镜像；只启动数据库后可单独执行迁移（读取同一套环境变量，不连接 Redis、不监听 HTTP）：

   ```bash
   docker compose build
   docker compose up -d postgres
   docker compose run --rm --no-deps backend ./epay-server migrate
   docker compose up -d --wait --wait-timeout 120
   ```

7. 重新保存通道配置并只启用实际支持的接口。JSAPI 必须提供 OpenID；商户通知地址必须可通过公网访问。历史未支付订单未保存支付参数，建议在服务商关闭后重新下单，避免用新通道配置重建旧支付。
8. 先用真实支付服务商的小额订单验证支付回调、商户通知、部分退款及退款查询，再恢复业务。检查状态为 `3` 的退款和后台日志；查单失败会继续冻结资金，需要修复通道凭证或核对服务商结果，不能直接释放资金。

`./epay-server migrate` 和正常启动都会执行同一份 schema 迁移。默认管理员只在管理员表为空时初始化；更改环境变量不会覆盖已有管理员密码。

## 故障排查

### 后端无法连接数据库

检查 PostgreSQL 是否就绪：
```bash
docker compose logs postgres
```

### 前端无法访问后端 API

检查 Nginx 代理配置和后端服务状态：
```bash
docker compose logs frontend
docker compose logs backend
```
