# SPEC-033 真实通知首次接收的候选诊断

2026-10-18；Android #77、epay-go #22；承接 #8/#33/#42，FR-03/04/05/07。本批实现默认构建中的 notification 接收诊断，不启用收款解析或生产自动确认。

## 行为

- 既有正常设备认证、来源范围、请求重放和事件原字节规则保持不变。不新增事件字段、接口、正文或商户密钥；events v1 ACK 的既有 `needs_review` 状态可直接使用。
- 仅首次正常认证接收的 `source_class=notification`，在接收外层事务和 receiver 锁内，复用首次候选快照，保存不可变 `notification-review-v1` 决定并将状态置为 `needs_review`。必须全部提交后才返回 ACK；任何失败全部回滚。精确重复返回原事件和原决定，不扫描新订单、不重判、不续期。
- 候选仍按已有 channel alias 的 environment/receiver/provider/精确金额及首次接收截止过滤，包括已付、退款和删除历史。零候选 `unmatched/no_candidate`，多个候选 `ambiguous/multiple_candidates`。本批不修改 alias/claim/下单规则，不注册生产 alias；当前线上未配置 alias 时零候选并不表示没有收款。
- 暂停优先记 `evidence_insufficient/confirmation_paused`；设备观察晚于接收超过2秒记 `clock_error/clock_skew`；接收晚于观察超过120秒记 `stale_observation/stale_observation`；更早同作用域同金额事件记 `duplicate_suspected/earlier_evidence`。这些是诊断条件，不证明设备时间或事件独立性可信。
- 唯一候选：已付/退款记 `duplicate_suspected/order_settled`；删除/非未付状态记 `evidence_insufficient/scope_changed`；观察早于创建记 `clock_error/observed_before_order`；只有订单已有明确截止（observer 或 checkout，两者取更早）才判 `expired/window_expired`。没有截止记 `evidence_insufficient/window_unconfigured`，不臆造生产10分钟政策；其余记 `evidence_insufficient/confirmation_not_enabled`。客户端 `distinct`、parser 名或平台流水字段不能绕过。
- 管理追踪沿用既有 policy/outcome/reason/candidate 字段；候选订单只用于解释，不能显示成已消费订单。暂停/恢复不重写已有终局决定。
- 新增迁移，不编辑001–006。数据库约束禁止 `notification-review-v1` 的 matched 决定，并核对新增决定与事件来源一致；该策略不能形成 consumption/收入。旧 notification 的 pending/历史决定不追溯改写。新策略不调用结算或商户回调。

## 验收

| 编号 | 要求 |
| --- | --- |
| REVIEW-01 | 独立PostgreSQL＋正常签名HTTP路由的合成输入：notification ACK明确needs_review，零/单/多候选、截止/时钟/历史原因可查，均零结算/回调 |
| REVIEW-02 | 同事件并发/新封套重复仅一个快照和决定；未来订单/状态变化/暂停恢复不能重判；存储失败全部回滚，失败后正常重试 |
| REVIEW-03 | 数据库拒绝review matched/来源串用、保留终局与消费约束；新增迁移可重入、漂移/未知版本仍失败；default及synthetic构建通过相关回归 |
| REVIEW-04 | 管理查询候选/已消费分开，不输出正文、设备凭据或原body；线上不部署/建alias/触发付款；SPEC-032实际窗口单独记录 |

本批测试输入均合成，即使使用 notification 枚举也不是模拟器真实App、真实收款或New API充值验收。真实模板仍缺，生产 SourcePolicy/ParserRegistry/OutboundPolicy保持关闭；#8/#33/#42保持开放。后续生产自动匹配政策另行实现与准入，不能复用synthetic标签绕过。

实施与验证见[接收诊断报告](notification-review-testing.md)。
