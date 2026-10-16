# SPEC-025 合成观察证据身份资格

2026-10-16；#16/#22/#30/#31，epay-go #7/#9；落实ADR-0005第4条。首次完整联调发现Android发送字节固定uncertain，网关按SPEC-022拒绝两个事件，零入账。这是尚未衔接的资格分类，不能通过网关忽略uncertain或改写旧body修复。

## 新事件规则

- distinct仅表示已验证夹具版本的一次独立实时观察，不表示真实支付证明。仅fixture source set提供资格；production恒uncertain且OutboundPolicy仍不允许发送。
- 资格在IdentityStore首次身份事务内确定：第一次出现的key、非恢复、已通过来源/结构/双渠道对应parser规则、合法时间、通知postTime不晚于接收wallTime且最多早120000ms、无粘性ingress_gap、有效synthetic绑定。任何条件不足仍uncertain或沿用既有不创建event规则。
- parser的词法Candidate本身保持identityUncertain=true；本地未绑定candidate payload保持uncertain。资格只在具备授权和持久身份上下文时加入新wire body。wire默认编码仍uncertain，调用方必须提供明确资格。
- exact重复复用已有event/body；修订、删除后重发按既有冲突/hold处理；恢复未知身份不创建event；历史UNBOUND永不补绑。更换资格规则不能重编码旧队列，不能把本次已被拒绝的事件/金额原地升级。
- 新key同内容仍是独立观察；网关永久金额claim、首次候选快照、历史更早证据阻断及消费唯一性保持不变，不代表能区分不可辨认的真实交易。
- 同key后续变更无法撤销已经完成的收入；本地能在发送前发现的冲突暂停队列，服务端已知冲突阻断匹配。合成生命周期测试结果不能升级为真实来源安全保证。

## 验收

- IDQUAL-01：真实Room首次双渠道fixture事件wire为distinct；精确重复/重开/重试body不变。
- IDQUAL-02：采集gap、新通知时间过旧/未来、错误scope/parser与production不得取得distinct；恢复未知不创建事件，旧UNBOUND不补绑；已有事件不因新规则升级。
- IDQUAL-03：原始AVD→普通订单→网关→New API重新用干净隔离数据执行；首次拒绝记录保留，不清洗其原事件和金额claim以重新消费。

本批不改变事件schema字段或网关准入枚举；两端同步规格并增加真实Android发出的distinct黄金字节/验签向量，避免仅手写服务端distinct测试掩盖集成缺口。
