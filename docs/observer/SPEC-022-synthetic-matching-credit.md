# SPEC-022 合成匹配与原子证据结算

2026-10-16；Android #22/#26、epay-go #7/#8；承接SPEC-003/004/021。只在明确隔离的合成构建/环境中处理；claim或单一待付单不构成真实支付证明。New API余额与持久回调端到端验证仍是后续验收。

## 输入与授权

入口只接受持久event内部PK，不接收order_id或候选提示。默认构建及缺少显式OBSERVER_SYNTHETIC_ENVIRONMENT时拒绝匹配；真实notification来源不准自动确认。事务依次锁environment→receiver→当前device/binding/credential→claim→event→order→merchant，接收事务不锁订单。

当前environment/receiver/device启用、原binding仍为单活跃版本、原credential active且在当前有效期、测试merchant授权与商户本身启用均必须满足；否则留下可解释的终局拒绝，不通过重新绑定或恢复扫描放行。source必须synthetic；仅微信fixture-wx-v1、支付宝fixture-ali-v1，identity_confidence=distinct，合成事件不携带provider_transaction_id。已有event_conflicts阻止自动处理。

授权认证在接收时完成，匹配不重新伪造请求/验签；必须存在与该事件first_key/first_request相连的已提交observer_request接收记录。直接RecordEvidence存储原语不构成可结算输入。

## 候选与时间

首次认证接收在receiver锁内保存服务端候选快照：当时的claim订单、符合截止时间的候选数量与唯一候选ID（若有）。该快照与request/event同事务且不可改写；重复请求不刷新。没有快照的旧event不追溯补建。这样事件先到、订单在同一毫秒后创建时也不会被补匹配。此ID仅由服务端数据库查询产生，不来自Android通知或调用方。

匹配按event的environment/receiver/provider/amount查询永久claim，并与首次快照逐项核对；同范围所有alias下、创建时间不晚于first_received_at的同额历史订单都参与候选计数，包括已付/退款/归档订单。不能只查未付单，也不能选最早/最近兜底。保存策略版本、候选计数、固定截止时间、决定/固定原因及唯一候选order关联。

首次快照零候选unmatched且不等待未来订单；当时有候选但当前集合改变则evidence_insufficient；多候选ambiguous；缺少claim或与唯一原订单范围不符evidence_insufficient；已付/已退款/已消费候选duplicate_suspected，零新增收入。原channel/provider/merchant与claim必须吻合。当前通道禁用/归档不丢弃此前窗口内已接收的合格证据；创建与Checkout依然按SPEC021拒绝新窗口/码。

时间规则沿用SPEC003：created_at <= first_received_at < expires_at，created_at <= observed_at < expires_at，-2000 <= first_received_at-observed_at <=120000ms。超未来偏差/观察早于订单/处理时钟倒退为clock_error，超过120秒为stale_observation，支付窗外expired。worker处理延迟不更新first_received_at，也不延长窗口。通知时间仍非平台可信时间。

明确拒绝决定一次持久化为needs_review，不自动重配/重评；没有成功处理决定的数据库错误则回滚并保留pending供恢复。冲突若在结算前提交则阻断；结算后才到达的矛盾内容独立保留审计，不伪称能够回溯撤销已提交账务。

## 同事务结算与恢复

matched决定、event consumption、order.paid、merchant balance、income ledger和notify_status=pending在同一事务提交。consumption分别UNIQUE(event_pk)、UNIQUE(order_id)，关联具体收入ledger；数据库延迟约束检查匹配决定/消费/订单/流水/credited状态一致，历史决定和消费不可改写或删除。抽取现有ProcessPayNotify的事务内收入函数，传统provider和observer复用；observer api_trade_no为NULL，不用event_id冒充流水。

已有相同event决定只返回原决定；新event_id/新通知key同内容遇已消费原单只记录duplicate_suspected。worker每次有界扫描pending并逐事件事务处理，可多实例安全重复；不在事务中发网络回调。提交前进程退出全部回滚，提交后退出/重启不重复收入。订单行现有notify worker负责持久回调，独立网络投递验证留后续批次。

processing_status增加数据库credited值（Android现有ACK解析已允许）；共享receipts向量增加credited重复ACK成功例。客户端仍只标“网关已接收”，不能把ACK的业务标签当成New API余额查询。

## 验收

- MATCH-01..12：双provider唯一/零/多候选、跨scope、历史重复、已退款、真实或未知来源、时间边界与未来订单，逐项断言零或一次收入。
- CREDIT-01：单event及同一order不同event并发，只一条consumption和一笔income；余额精确增加amount-fee，原金额不变。
- CREDIT-02：决定/消费/订单/余额/流水写入及延迟commit故障全部回滚；实际子进程在提交前/后退出并恢复，不能只用mock证明崩溃。
- CREDIT-03：缺已认证request、binding换代/credential撤销、parser错误/uncertain/冲突、默认构建均零入账。
- CREDIT-04：持久审计能关联event→decision→order→ledger及pending callback；credited后原事件重复HTTPS ACK仍严格合法，不新建收入。
- CREDIT-05：既有provider结算/退款/并发账本回归通过；没有真实付款、New API充值或商户通知成功证据时明确Not run。
