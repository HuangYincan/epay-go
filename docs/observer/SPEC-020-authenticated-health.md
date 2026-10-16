# SPEC-020 设备认证健康检查

2026-10-16；#24及epay-go #6。仅证明指定设备凭据在检查时仍可用于对应授权范围；不证明监听来源健康、平台到账或充值。与SPEC-019组成状态/诊断批次。

## 固定协议

仅独立TLS监听器的POST `/api/observer/v1/health`，无query/redirect/路径变体；JSON体最多64字节，仅整数schema_version=1，无重复/未知/null/尾随内容。客户端固定原字节 `{"schema_version":1}`。沿用事件封套单值header、32字节设备key、5分钟偏差与独立request_id，每次检查新封套。

签名域明确隔离为 `["epay-observer-health-v1","POST","/api/observer/v1/health",key_id,sent_at,request_id,sha256(body)]`，LF连接、无末尾LF。健康签名不能用于events，反之亦然；不通过提交伪造/故意非法事件测试连接。双方共享health.json黄金向量先于实现提交。

网关复用接收端事务内environment→receiver→device/binding/credential授权锁序，检查enabled/active/时间/来源环境及KeyCipher完整AAD；签名成功后扣同一持久device预算。仅预算允许变化，绝不创建event、event request、消费、订单、账本或通知任务。重复health封套仍重查当前撤销/时间并计费；纯检查不登记事件重放表，不产生可重复执行的业务副作用。

成功200、精确字段 `{"schema_version":1,"status":"authorized","server_time_ms":<整数>}`。错误复用SPEC-017的严格code与HTTP语义；限流429包含Retry-After。返回成功前事务必须提交，数据库失败503。200不是事件ACK，不可送入OutboxStore.acknowledge。

## Android交互

单独按钮“检查网关授权”，对当前各渠道活跃本地凭据使用KeyStore签名，不需要候选事件。缺KeyStore/过期/配对进行中时不发送。完成后再次比较本地凭据快照；期间轮换/修复则显示状态已变化，不把旧结果当作新授权。网络、TLS、未知/错误响应均固定错误说明，不输出请求/响应/异常内容。

检查不修改事件状态、重试时钟、upload hold或本地绑定，也不自动恢复上传。显示每渠道“授权检查通过/拒绝/未完成”，并持续注明它不证明收款App正在投递通知或充值成功。用户另外执行修复/配对/重试；不能用健康检查绕过暂停。

## 验收

- HEALTH-01：Kotlin/Go黄金签名相同，双向跨路由/域拒绝；严格body/header/response负例。
- HEALTH-02：实际TLS成功/过期/撤销/错误scope；数据库event/request/order/ledger计数不因检查变化；持久限流和提交失败无成功响应。
- HEALTH-03：Android实际KeyStore与TLS；本地队列/hold字节前后不变，轮换晚到结果失效，缺key不发请求。
- HEALTH-04：UI将授权检查、监听与ACK/业务状态分开；Release没有新增来源或测试信任旁路。
