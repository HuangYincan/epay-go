# SPEC-026 管理端追踪与自动确认暂停

2026-10-16；Android #28/#32/#36，epay-go #8。完成运行手册中按receiver暂停但保留证据的要求。仅现有专用HTTPS管理员JWT可调用；不得提供手工把通知改为到账的入口。

## 管理协议

- `POST /api/admin/observer/events/query`：严格JSON `{schema_version:1,receiver_id,before_event_pk,limit}`；before为0表示最新，limit为1–100。作用域由专用服务启动配置固定，不接受body环境/actor/商户权限。返回该receiver的分页事件状态、金额/规则/资格、匹配原因和策略、消费/账本/订单/通知状态及最近20条暂停操作记录；不返回原body、通知、密钥、封套或设备标识。
- `POST /api/admin/observer/confirmation`：严格JSON `{schema_version:1,receiver_id,paused,reason_code}`，reason仅`incident`、`maintenance`、`review_completed`。记录已认证且未删除管理员ID、时间、操作和固定reason；同事务变更暂停状态。客户端不得提供actor或人工订单指派。
- 暂停不同于吊销设备或禁用receiver：仍正常认证、存储事件并返回持久ACK。ACK继续不表示充值。

## 原子边界与历史

- 控制操作沿用environment→receiver锁顺序，与接收/匹配串行化。暂停时，已pending事件在同一事务冻结为needs_review/evidence_insufficient/confirmation_paused，并保留候选快照、原字节、事件/金额历史；清理其匹配退避。已credited或终局事件不变。
- 暂停期间新事件仍以正常身份/防重放事务接收；在首次候选快照之后同事务写入confirmation_paused终局决定，因此即使worker暂停或管理员立即恢复也不能稍后自动入账。
- 恢复仅允许未来新观察按原规则处理；历史终局决定不可升级，同金额永久claim和更早证据保护不改变。恢复不是人工确认付款；真实异常需要独立真实账单核对，当前原型没有手动充值旁路。
- 控制状态与操作记录持久保存；操作记录不可改删，控制状态引用对应最新操作。任何失败全部回滚，不能留下部分暂停或未审计控制状态。暂停不撤销已提交收入，也不阻止其商户回调完成。

## 验收

- ADMIN-01：无JWT/商户JWT/错服务origin拒绝，错误或额外字段拒绝，管理员身份不来自body；分页与scope隔离，响应无敏感字段。
- ADMIN-02：已有pending、暂停中新事件保持持久接收但零收入；立即恢复、重启和重复处理不能升级旧事件。未来不同首次金额可按普通流程充值。
- ADMIN-03：暂停与接收/匹配并发只可能“提交前已入账”或“冻结待核对”；无中间消费/收入。失败注入控制/审计原子回滚，迁移旧数据与重复启动可用。
- ADMIN-04：实际已运行lab的管理员HTTPS查询可展示逐笔关联；暂停/恢复及操作审计可查询。生产自动匹配继续关闭。
