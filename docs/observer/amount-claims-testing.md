# M3 永久金额占用子批次

2026-10-16；Android #18、epay-go #7，SPEC-021 CLAIM-01..06。证据层为**隔离PostgreSQL合成订单服务测试**，本批不运行AVD通知、不匹配事件、不记账或调用New API。

## 实现

普通Create/Checkout接入原金额永久claim，唯一键为environment/receiver/provider/amount_minor，order_id亦唯一。订单和claim同事务提交；独立10分钟窗口不续期，过期/已付/退款/新binding/通道归档不释放。所有同范围历史订单均参与拒绝，无claim历史不升级。默认构建不能启用，专用observersynthetic构建还必须显式指定环境，并通过环境/receiver/测试merchant授权检查。

数据库延迟约束拒绝缺claim的observer订单，claim禁止改写/删除，原订单范围/金额/窗口冻结。alias注册与claim按同一receiver串行化，不能在窗口后加入隐藏的历史同额冲突。缓存QR返回前核对窗口、启用范围和原通道；已展示静态码无法阻止后续付款，因此历史claim永久保留。二维码渠道仅取原order.provider，不相信请求Extra。旧ProcessPayNotify/查单不能确认observer订单，不伪造平台流水。

## 已执行证据

固定Go/PostgreSQL容器沿用M2；每个测试独立schema，结束自动清理，无宿主工具安装。

- 默认构建全仓go test -race：206项（含子测试），0失败/跳过；默认环境变量+数据库白名单仍拒绝。
- observersynthetic全仓race：236项（含子测试），0失败/跳过；随后新增确定顺序alias回归，最终service包73项、含15个顶层TestSynthetic测试全部通过。两构建vet均通过。
- 16并发跨alias/merchant同额仅一个普通订单/claim，8并发同一幂等请求仅原窗口；微信/支付宝金额空间独立。
- 严格10分钟边界与倒退时钟，禁用receiver/merchant/原channel、归档后缓存QR拒绝；原金额10.01元不变。
- 已付/退款状态由测试明确准备，仅验证这些状态不会释放claim，不冒充真实记账或退款执行。
- 延迟提交异常使订单与claim一起回滚，撤除该测试故障后原请求可重试；数据库拒绝孤立窗口、claim更新/删除及已claim订单金额/期限变更。
- alias先登记历史、claim先提交后尝试登记历史、4轮真实并发竞争均安全；并发测试不声称每次必定覆盖两种获锁顺序，确定顺序由独立用例保证。
- 候选通道列表读取后立即归档原observer通道，存在legacy候补时仍拒绝且0订单/claim，避免静默回落。

[脱敏计数与测试名](amount-claims-evidence.json)。原始jsonl只保留/tmp/observer-claims-final-*；不上传数据库连接凭据或测试服务器私钥。最初测试曾因测试商户重复API Key及未来迁移版本号未更新而失败，修正夹具唯一值和未来版本后重跑通过，没有放松约束。

## Standards

原P1：列表读取后归档导致回落legacy候补。6a8dbb8修复并增加确定时序回归，复审无新增实质问题。

## Spec

原P2缓存QR绕过通道禁用、P1新增alias引入历史同额冲突均修复；6a8dbb8复审确认。追加“claim先提交”确定顺序测试通过。

## 版本与下一批

网关规格58cf148、实现455794e、修复6a8dbb8；Android规格c7ea7e4。两端最终PR/CI在合并时记录。#18保留跨链路金额/回调验收追踪，#22/#26仍未实现；下一批必须同事务完成匹配、证据消费、原订单支付和收入账本，之后验证持久回调/New API实际测试余额一次性增加。生产自动匹配仍关闭，Goal active。
