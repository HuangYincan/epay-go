# EPay Go

一个基于 Go + Gin + PostgreSQL + Redis + Vue 3 的支付系统示例项目，提供管理后台、商户中心、统一下单、通道管理，以及订单 / 退款 / 结算流程。

## 技术栈

- 后端：Go 1.26.8+、Gin、GORM、PostgreSQL、Redis
- 前端：Node.js 22、Vue 3、Vite、Arco Design
- 部署：Docker Compose、Nginx / Caddy

## 目录说明

- `cmd/server`：服务启动入口
- `internal`：后端核心业务
- `web`：前端代码
- `deploy/nginx`：Nginx 配置示例
- `deploy/caddy`：Caddy 配置
- `docker-compose.yml`：默认部署
- `docker-compose.prod.caddy.yml`：Caddy 直接对外的生产部署示例

## 快速开始

### 1. 准备环境变量

```bash
cp .env.example .env
```

配置数据库与 Redis 的独立密码，并填写以下必需项：

- `JWT_SECRET`：至少 32 字节的随机密钥，可用 `openssl rand -hex 32` 生成。
- `DEFAULT_ADMIN_PASSWORD`：首次初始化需要至少 12 个字符的独立密码，可用 `openssl rand -base64 24` 生成。

配置缺失或使用已知示例密钥时，后端会拒绝启动。支付渠道在管理后台配置。

### 2. 启动项目

```bash
docker compose up -d --build
```

默认包含以下服务：

- `postgres`
- `redis`
- `backend`
- `frontend`

默认只发布宿主机回环端口，公网访问需配置反向代理或隧道：

- `127.0.0.1:8081`：前端（容器内 Nginx 同时代理支付 API）
- `127.0.0.1:8080`：后端
- PostgreSQL、Redis 只在 Compose 网络内访问，不发布宿主机端口

### 常用访问入口

本机访问 `http://127.0.0.1:8081`，或通过配置好的公网域名访问以下前端路径：

- **管理员登录**：`/admin/login`
- **商户注册**：`/merchant/register`
- **商户登录**：`/merchant/login`

## 环境变量

参考 `.env.example`。常用变量包括：

- `DB_USER`
- `DB_PASSWORD`
- `DB_NAME`
- `REDIS_PASSWORD`
- `JWT_SECRET`
- `DEFAULT_ADMIN_USERNAME`
- `DEFAULT_ADMIN_PASSWORD`
- `SITE_ADDRESS`
- `ACME_EMAIL`

> ⚠️ 支付渠道（支付宝、微信、汇付天下）的 AppID / 商户号 / 密钥等**不在 `.env` 中配置**，而是登录管理后台后在「通道管理」里按通道填写、保存到数据库。`.env.example` 中残留的 `ALIPAY_*` / `WECHAT_*` 变量后端已不再读取，可忽略。

系统首次启动且数据库中没有管理员时，会使用 `DEFAULT_ADMIN_USERNAME` 和 `DEFAULT_ADMIN_PASSWORD` 初始化默认管理员。

## 部署

### Caddy 直接对外

适用于宿主机未占用 `80/443`：

```bash
docker compose -f docker-compose.prod.caddy.yml up -d --build
```

### 宿主机 Nginx 反向代理

适用于宿主机已有 Nginx：

```bash
docker compose up -d --build
```

然后由宿主机 Nginx 反代到容器端口，示例配置见：

- `deploy/nginx/host.prod.conf.example`

## 支付参数说明

已支持的支付通道（均在管理后台「通道管理」中配置密钥并启用）：

- **支付宝官方**（plugin: `alipay`）
- **微信官方**（plugin: `wechat`）
- **汇付天下 / 斗拱聚合支付**：拆为两个独立插件——`hf-wxpay`（汇付-微信，支持扫码 / JSAPI / H5）和 `hf-alipay`（汇付-支付宝，当前仅扫码）。客户端传 `type=wxpay` 会路由到 `wechat` 或 `hf-wxpay` 通道，传 `type=alipay` 会路由到 `alipay` 或 `hf-alipay` 通道；具体走官方还是汇付，由后台通道的启用状态与排序（`sort` 升序优先）决定，对商户透明。

支付通道和支付场景是分开的：

- `type` / `pay_type`：决定渠道，例如 `wxpay`、`alipay`
- `pay_method`：决定场景，例如 `native`、`scan`、`h5`、`jsapi`、`web`

### 关键规则

- `type=native` **不允许单独使用**，因为无法判断是微信还是支付宝
- 后端支持显式别名，并会自动归一化：
  - `WX_NATIVE`
  - `WX_JSAPI`
  - `WX_H5`
  - `ALIPAY_SCAN`
  - `ALIPAY_H5`
  - `ALIPAY_WEB`

### 推荐传法

- **微信 Native**
  - `type=WX_NATIVE`
  - 或 `type=wxpay&pay_method=native`

- **微信 JSAPI**
  - `type=WX_JSAPI`
  - 或 `type=wxpay&pay_method=jsapi`
  - 必须同时传 `openid`，它和 `pay_method` 都需要参与 MD5 签名；OpenID 应属于通道配置的微信 AppID。

- **支付宝扫码**
  - `type=ALIPAY_SCAN`
  - 或 `type=alipay&pay_method=scan`

- **支付宝 H5**
  - `type=ALIPAY_H5`
  - 或 `type=alipay&pay_method=h5`

- **支付宝网页支付**
  - `type=ALIPAY_WEB`
  - 或 `type=alipay&pay_method=web`

## 账务与通道规则

- **费率单位为百分数**：填 `0.6` 表示 `0.6%`，100 元订单手续费为 0.60 元。最终金额保留两位小数。
- 日限额按服务器本地日期统计该通道当天已创建订单的总金额；未支付订单也占额度，以防并发下单超限。日限额 `0` 表示不限额。
- 下单只允许通道启用的接口。收银台保留原订单的通道、支付方式和金额，重试返回已有支付参数。
- 商户通知地址只允许公网 HTTP/HTTPS，禁止内网、回环、云元数据地址及重定向到这些地址。
- 同一商户的订单号不可重复；数据库唯一索引提供最终约束。
- 退款状态：`0` 待审核、`1` 成功、`2` 失败、`3` 上游处理中或结果待确认。审核通过先冻结可用余额；网络超时保留冻结资金，后台自动查单，不会把处理中当作成功。
- 部分退款后订单继续保持已支付；累计成功退款等于订单金额时才标记已退款。尚未失败的退款申请也计入可退款额度。
- 结算状态：`0` 待审核、`1` 待打款、`2` 已完成、`3` 已驳回。管理员实际打款后再点“确认已打款”；此按钮只完成账务结算，不向银行或支付宝发起转账。

微信支付回调须先通过平台签名验证，再用 APIv3 密钥解密并校验 AppID、商户号及金额。后台可配置微信支付平台公钥及公钥 ID，或平台证书及其序列号；均留空时由 SDK 下载平台证书。新订单的回调地址包含通道 ID：`/api/pay/notify/{plugin}/{channel_id}`。旧地址仍兼容并按验签结果及订单所属通道匹配，禁用通道仍可接收历史订单回调。

汇付通知也会校验通知中的汇付商户号。`00000100` 表示请求已受理、交易仍处理中；退款保留冻结资金直到查到最终结果。

已有站点升级前，参见 [部署指南](DEPLOYMENT.md) 的升级核对步骤。

## 构建说明

如果在中国大陆网络环境构建，可以在 `.env` 中设置：

```env
GOPROXY=https://goproxy.cn,direct
NPM_REGISTRY=https://registry.npmmirror.com
```

- `GOPROXY`：加速后端 Go 依赖下载。
- `NPM_REGISTRY`：加速前端 npm 依赖下载，前端镜像默认已使用该阿里云镜像。

> 注意：`web/package-lock.json` 里的依赖下载地址（`resolved`）会被写死，若该文件在配了内网镜像（如腾讯云内网 `mirrors.tencentyun.com`）的机器上重新生成，会导致其他环境 `npm ci` 因地址不可达而失败。重新生成锁文件时请确保使用公网可达的镜像。

## 支付入账回归测试

订单入账使用 PostgreSQL 行锁和单一事务，保证重复回调与主动查单同时触发时只入账一次；订单状态、商户余额和资金流水一起提交或回滚。同一商户多笔订单的流水使用锁定后的余额，并全程保留 decimal 精度。

运行普通测试：

```bash
go test ./...
```

并发和回滚测试需要独立的 PostgreSQL 测试数据库。设置连接后运行（未设置时这些集成测试会跳过）：

```bash
EPAY_TEST_DATABASE_DSN='host=127.0.0.1 port=5432 user=epay_test password=test-only dbname=epay_test sslmode=disable' \
  go test -race ./...
```

测试用户需要创建 schema 的权限。每个测试创建并清理自己的 schema，不读取已有业务表。GitHub Actions 使用 PostgreSQL 16 自动执行这些测试、静态检查、Go 漏洞扫描、前端依赖审计及前后端镜像构建。

