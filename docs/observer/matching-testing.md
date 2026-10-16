# M3 合成匹配与事务入账子批次

2026-10-16；Android #22/#26、epay-go #7/#8；SPEC-022及SPEC-003/004。证据层：隔离PostgreSQL、真实Go子进程退出及Go实际TLS；本批没有运行AVD完整充值、商户通知投递或New API余额验证。

## 事务与候选

首次认证接收在receiver锁内冻结服务端候选数量、唯一候选ID（若有）和claim关联，与event/request同事务。调用方和通知均不提供订单ID。此快照解决事件提交后、同一毫秒才新建订单的回溯匹配问题；旧记录没有快照不能补建。

匹配重查当前环境/receiver/device/binding/credential、fixture parser/distinct身份、已认证request、冲突摘要和测试merchant授权。原金额/窗口及所有历史订单参与决定；更早同scope/金额事件阻止后来的新event_id绕过未知/无候选历史。默认构建不能创建matcher，notification来源即使处于专用构建也不能入账。

matched决定、唯一event/order消费、原订单paid、merchant余额、具体income流水及notify_status=pending一起提交。传统provider与observer共享事务内收入函数；observer api_trade_no为NULL，不编造平台流水。数据库延迟检查覆盖event INSERT/UPDATE及决定/消费，收入流水和claim费用不可改写。退款历史不会被恢复为paid。

每批最多50条/20秒，每事件5秒；失败持久退避60秒指数增长至15分钟，按到期时间排序。短扫描SKIP LOCKED后先释放event锁，再走环境/receiver→授权→claim→event→order→merchant锁序。退避登记仅event→retry，不反向等待receiver；晚到失败复查pending，成功/终局拒绝同事务删除退避。

## 已执行验收

- 最终默认全仓race 207项、专用构建295项（执行项包含子测试与进程helper），0失败/跳过；两构建go vet通过。Android production/fixture JVM通过新增credited重复ACK共享向量，仍只标持久接收。
- 微信/支付宝分别在真实数据库完成唯一匹配：原金额10元、费用0.10元、商户3.25→13.15元，一条consumption和一条income，原平台流水为NULL，通知状态pending。这里只是网关合成账本，不是New API余额。
- 同event及12个不同event并发均只有一笔收入。相同事件重试保持原决定；新通知key/event_id触发历史重复疑似，不重复收入。
- 候选0/1/2、缺claim、paid/refund准备状态、其他provider、notification来源、错误parser/uncertain/伪平台引用、冲突、撤销/换binding/禁用、原始存储原语缺认证request全部有明确拒绝。
- 10分钟最后一毫秒/精确到期、未来偏差2000/2001ms、离线120000/120001ms、观察早于订单、服务时钟倒退、同毫秒晚建订单及其后续新event不能清除历史均测试。
- order/merchant/ledger/decision/consumption/event各写入阶段及延迟commit故障均整体回滚，保留pending后重试恰好一次。
- 真实子进程在income INSERT之后、commit之前os.Exit(42)，无部分账务；commit后再os.Exit(42)，新matcher恢复也无第二笔收入。不是mock或只抛异常替代进程退出。
- 真正TLS HTTP：202 pending持久接收→事务入账→新封套200 duplicate/credited，首次received_at不变。该TLS由Go测试客户端完成，不冒充Android通知全链路。
- 实际4条receiver锁等待前缀耗尽20秒后，重建matcher仍能在锁未释放时结算后续独立支付宝范围；预锁event及扫描后加锁event（5秒处理+1秒退避登记失败）均不会截断独立范围。恢复测试中显式把协调退避准备为已到期，不把它写成真实等待60秒。
- 裸INSERT credited、没有完整审计而UPDATE credited、删除消费、改写决定/终局状态/收入流水/费用均被数据库拒绝。

[脱敏测试计数及名称](matching-evidence.json)。私有原始日志为/tmp/observer-matching-final-*.jsonl；固定Go/PostgreSQL镜像和隔离schema清理沿用前批，无真实账户/资金。初轮子进程测试有os/exec命名冲突，修正导入别名后实际执行通过。

## Standards

原恢复扫描饥饿及credited INSERT约束缺口已修复。复审进一步发现event行锁导致退避登记失败会截断批次；8e0524b增加短事务SKIP LOCKED、协调失败继续和两种确定时序回归，最终复审无新增实质问题。

## Spec

原credited INSERT缺口、后续event锁饥饿均关闭；首次候选冻结、历史证据、锁序、收入原子性及默认构建拒绝符合规格。未越界宣称商户回调或New API充值。

## 版本与边界

网关规格8909c65/6a1775a，实现35ad7be，修复466c850/8e0524b。Android规格c845cc8/44fd270及共享receipt向量。最终PR/CI在合并后记录。

#22/#26保留AVD/跨链路验收追踪；#27/#29持久商户投递与受限本地网络、#10 New API金额核对、#30–32双渠道实际余额/故障矩阵/干净复现仍未交付。New API固定633b4c5已建立/tmp/epay-observer-new-api独立checkout，尚无补丁或运行证据。生产自动匹配关闭，完整Goal active。
