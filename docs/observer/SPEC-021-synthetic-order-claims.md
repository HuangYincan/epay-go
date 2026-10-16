# SPEC-021 合成订单永久金额占用

2026-10-16；Android #18、epay-go #7；落实SPEC-003已确定的synthetic-exclusive-v1。该批只发放普通测试订单的支付窗口，不声称已完成匹配/入账或New API充值。

## 启用与范围

默认Go构建不能启用合成订单策略；必须以`observersynthetic`构建标记编译，并显式设置`OBSERVER_SYNTHETIC_ENVIRONMENT`。数据库环境必须为enabled synthetic，receiver为enabled，merchant在该环境的独立测试商户授权表中enabled，通道具有不可变receiver/provider alias。缺少任一条件拒绝，不回落到旧epay回调模式或另一通道。默认生产构建不因配置变量存在而放行。

同一环境的测试merchant不得映射多个环境；授权记录和claim只允许禁用或保留历史，不删除/改写范围。通道plugin仍为epay，沿用普通Create/Checkout和原静态码配置；测试码只含合成内容，不含订单ID。未登记observer alias的既有通道行为不变。

## 创建与收银台

普通下单事务先锁环境/receiver/测试商户授权，后锁通道/商户；新订单在提交前不可见。receiver行串行化同范围创建及alias注册；新alias若引入与永久claim同额的历史订单则拒绝注册，不能在已发窗口后增加隐藏冲突。通道锁后重新核对alias，防止配置并发导致漏占用。相同merchant/out_trade_no且原金额、provider、名称、通知/返回地址和显式通道一致时可返回原observer订单，不续期；不同业务参数拒绝。

订单与永久claim在同一事务提交，金额键为(environment,receiver_id,provider,amount_minor)，order_id亦唯一；保存原channel、merchant与created_at_ms，observer_pay_expires_at固定created_at+600000ms。唯一冲突或该范围下存在任何历史同额订单时，整个创建回滚，显示“此金额当前不可使用”，不改金额、不展示码、不尝试其他通道。使用整数分/精确decimal，Amount=RealAmount保持原值。

claim禁止UPDATE/DELETE，已支付/退款/过期/设备轮换/别名新增或归档均不释放。数据库核对claim与订单金额、范围、时间一致；延迟约束保证observer订单不能单独提交，已有claim的核心订单字段不能改写。无claim的历史订单不能追溯升级为可支付observer订单。

Checkout在返回缓存QR前检查启用范围、原claim和严格created_at <= now < observer_pay_expires_at；时间倒退、窗口过期、缺claim、原通道禁用/归档和非测试构建均拒绝。原QR已经展示后无法阻止静态码付款，永久claim仍保留。旧ProcessPayNotify及主动查单入口不得确认observer订单（包括新增alias后的历史订单）；后续只能经SPEC-004原子证据结算入口入账。provider流水保持缺失，不伪造。

锁序：创建不锁已有订单；只读取不可变幂等字段，返回后另开Checkout事务。Checkout只锁原订单并只读范围，不能反向锁receiver；结算后续按环境/receiver→claim→event→order→merchant执行。新订单无需与其他结算事务争用尚不存在的order行。

## 验收

- CLAIM-01：真实PostgreSQL并发同额仅一个窗口；多alias/merchant不绕过，同provider/account范围隔离。
- CLAIM-02：同一幂等请求返回原单且不续期；参数改变拒绝；金额/收银台值保持原分值。
- CLAIM-03：过期、已付、退款、换绑定、软删/恢复alias通道后同额仍拒绝；历史无claim同额也拒绝。
- CLAIM-04：任何创建错误/延迟提交失败不遗留订单或claim；约束拒绝删除/改写claim、只建窗口不建claim、改写已claim订单。
- CLAIM-05：缓存QR在精确10分钟边界、时钟倒退、禁用范围时不返回；旧签名回调无法给observer订单记账。
- CLAIM-06：默认构建即使配置环境与数据库白名单也拒绝；专用构建无显式环境/错误环境同样拒绝；既有非observer支付回归保持通过。

后续#22/#26必须在同一事务重新检查候选/消费/入账；本批没有允许用claim或单一待付单直接证明支付。
