# 双渠道模拟器合成充值最终验收

2026-10-16；本库#6–9，对应HuangYincan/epay-observer-android #10/#17/#18/#22/#26–32/#34/#36。SPEC-016–026。真实微信/支付宝、真机与生产自动匹配未准入。

网关`9a96f72`，Android资格修复`8ff7f40`与最终联调分支，New API基线`633b4c5b190dc1af99a2814391354d86948e7943`加本地`42c3424d3ce3b82d71668dba2b84f58315d7d078`金额补丁（AGPL，未向上游推送）。本库不复制New API实现。

- 默认全仓race211项、observersynthetic322项，0测试失败/跳过（每构建另有14个无测试文件package）；双构建vet通过。真实PostgreSQL包含20并发匹配、控制暂停/接收/结算串行、审计不可变、事务故障与退款状态追踪。
- 新空lab普通New API订单经submit创建，独立App双渠道10元通知经Android实际HTTPS上传/验签/匹配/持久收入/回调，quota100,000,000→110,000,000。
- 双渠道20元故障单：首次503后SIGKILL网关恢复；New API已提交丢回包后两进程SIGKILL恢复；最终quota130,000,000，四条消费/收入/成功topup。
- 当前商户key轮换、合法签名错金额、错签、缺金额、未重签篡改、晚到重复和20并发实际TLS回调均无额外额度。新key相同内容为duplicate_suspected。
- 现有lab schema5→6升级，四笔账务保持；实际管理员TLS暂停40元新通知仍ACK但needs_review/confirmation_paused，恢复及再次启动不升级，候选单仍未支付，quota不变。候选与已消费订单字段分开，不提供手动到账入口。
- 原uncertain闭环失败证据原样保留，未重写历史event/body；SPEC-025仅fixture首次持久新鲜资格，真实notification继续拒绝自动确认。default safehttp、TLS、签名及凭据隔离不变。

完整版本/分层故障矩阵、逐笔跨数据库审计、私有产物SHA和从空数据启动/清理步骤在Android仓库：

- `docs/testing/M3-prototype-report.md`
- `docs/testing/recharge-v1/evidence/`
- `docs/operations/prototype-demo.md`
- `scripts/prototype/`

永久金额claim属于保守合成政策；真实付款归属仍由Android #8跟踪。默认/生产构建不获得合成自动匹配或环回回调能力。公网HTTPS部署、双活完整New API实例及真实退款产品流程未运行，不把分层测试包装成这些结果。最终PR/CI记录在两仓库PR及Android报告交付记录。
