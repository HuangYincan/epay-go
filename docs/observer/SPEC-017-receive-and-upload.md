# SPEC-017 事务接收、实际上传与 ACK

2026-10-16；#20/#21/#25、epay-go #6。实施契约，测试通过前不声明接收/上报完成；沿用SPEC-002事件字节/签名域，不改变已交付黄金向量，不增加订单提示。

## 接收事务

仅在SPEC-016独立TLS监听器注册固定POST /api/observer/v1/events；启动固定environment/source_class。请求16384字节、单值封套、禁止压缩/query/路径变体。HTTP全局预算及并发限制先保护入口；设备预算持久化。

按key_id读取不含明文的凭据范围，再锁environment（SHARE）→receiver（UPDATE）→device/binding/credential，与配对/撤销/轮换事务保持一致。所有enabled/active/state/not_before/expires以及source_class在事务内重新核对。decrypt必须通过完整范围AAD，临时明文仅用于既有VerifyRequest并清零；原字节授权范围与不可变credential一致。配置不符或失效授权不登记事件。

请求唯一(key_id,request_id)，保存封套摘要及结果引用，不以event_id替代request_id。相同request且封套不同409 request_conflict；相同封套仍重验签名、时间和当前授权，再返回原事件的duplicate或原冲突，不绕过撤销。记录首轮不清理，避免时钟回退与清理窗口风险；以后保留策略单独实现。

授权/预算/request/event/pending在同一个READ COMMITTED事务中提交。复用RecordEvidence的嵌套savepoint不能提前ACK。相同event原字节不同409 event_conflict，同时持久保存冲突摘要；同event同body200 duplicate，首次202 accepted。数据库失败全部回滚并503，不返回持久ACK。事件首次key/request/received_at冻结，重复ACK取数据库首次接收时间及当前processing_status。

每device持久token bucket：容量20、每秒1个token（60/min），整数毫token运算；时间倒退不补充额度，不用浮点。重放/重复请求同样计费；429 Retry-After按补充到一个token的剩余时间计算。状态事务与接收一致；不得因预算错误吞事件。

## Android实际发送

从Room领取lease，按该scope获取当前非秘密credential与KeyStore句柄；缺密钥、过期、非当前绑定不发送。每次尝试调用RequestSigner生成新的request_id/sent_at，body始终是队列原字节。HttpsURLConnection正常系统/显式测试域CA校验，禁止redirect、压缩、跨origin与不受限响应。

严格解析ACK字段集合：schema_version/event_id/receipt_status/processing_status/received_at_ms，拒绝重复/未知/null/浮点/尾随内容；只有202 accepted或200 duplicate且event_id吻合才调用OutboxStore.acknowledge。网络/5xx/非ACK 2xx按RetryPolicy原事件重试；429尊重Retry-After。400/413/415按已知协议错误终止该事件，未知错误保守重试。401/403暂停对应授权发送并保留证据；409隔离冲突。所有结果均带lease token，晚到结果不能改写新租约/已经暂停的事件。

先实现可显式触发的真实投递循环与恢复验证；WorkManager和重启调度由#23接入同一个可测试的投递入口，不另建内存队列。界面将ACK与充值状态明确区分；此批不匹配订单、不更新余额。

## 验收

- RECEIVE-01：实际TLS原字节签名与三黄金向量；篡改/越权/过期/撤销/跨环境拒绝，数据库零副作用。
- RECEIVE-02：并发同event、同request、封套冲突、事件冲突；首次记录保持不变。
- RECEIVE-03：deferred commit失败无ACK；服务重启/ACK丢失后恢复只产生一份事件；重放仍检查撤销。
- UPLOAD-01：实际KeyStore签名，经HTTP验签/落库/ACK后Room ACKED；每次新request，body/event固定。
- UPLOAD-02：断网、429、5xx、错误ACK、redirect、401/403/409及晚到租约结果遵循既有策略。
- UPLOAD-03：双渠道合成通知经正常采集/持久化/配对凭据发送，历史UNBOUND不补绑；不注入订单字段，不宣称已支付。

测试先补共享ACK/错误向量，再实现双方；默认Release来源资格和safehttp不变。真实支付匹配仍关闭。
