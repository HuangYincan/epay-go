# 网关schema6→8休眠升级与兼容回退

2026-10-19；Android #82 / epay-go #30，SPEC-045。沿用既有网关受控升级授权；不改New API，不启用规则或真实结算。

## 实际原版本与回退问题

从运行容器只读取得实际二进制，Go buildinfo证明revision为`e8b18149d83ca91028696d7f7850750d690dbc9a`且modified=false，架构linux/amd64、Go1.26.9；不是仅凭镜像标签猜测。现网observer schema6、一个有效绑定/凭据，预检无待发送回调。

已合并候选`33d5f08ef98840f7ddc063bd323c2dabe21d164b`使用schema8。原二进制迁移器严格拒绝超出自身清单的版本，不能把“保留旧镜像”当作迁移8后的可用回退。

私有兼容回退提交`acdd3eefa67cccbf511884f61229a4f611e6c5ac`以实际原revision为base，仅引入候选的migrate.go、007和008SQL，三文件逐字一致，001–006及旧业务代码不变。保留未知版本/校验和拒绝。它只适用于本批rule/pilot均0的休眠阶段；未来已arm或曾启用试验不能沿用本结论，不删历史来满足条件。

## 隔离实际二进制演练

独立PostgreSQL16/Redis7，无宿主公开端口、无生产数据。实际原、候选、兼容回退二进制在原runtime镜像中运行：

1. 原二进制迁移到6，创建明确合成的旧商户/通道/订单/环境。
2. 候选迁移到8；原二进制在8按预期拒绝，失败日志保留。
3. 兼容回退迁移/健康接口成功，切回候选健康成功；所有合成旧行与前6个checksum一致。
4. 独立合成DB副本注入未知9或坏checksum，兼容回退均拒绝；主演练历史保留。

8个阶段通过，rule/pilot/callback均0。不是M3合成充值重跑，也不证明真实支付宝兼容或New API增额。

构建只使用固定go.mod/go.sum依赖、默认构建无synthetic标签；首次无网络缺依赖失败及并行下载临时缓存失败保留，后续candidate run3/backout run2成功，未放宽校验。候选与回退镜像均沿用实际原runtime base，只有二进制/本地operator工具和来源标签改变；Go1.26.9/CGO0/linux/amd64/trimpath。

## 切换准备与实际结果

私有产物与证据在`/private/tmp/observer-gateway-rollout-20261019`，包含实际原镜像/二进制、两个候选镜像、源码/补丁/manifest/hash、演练报告和失败日志。服务器私有保存原配置/必要KEK与TLS材料引用、一致性pg_dump归档及list/hash；归档可读不是实际生产数据恢复演练。

切换脚本要求准备与当下完整resolved compose一致，预验证candidate/schema6/schema8三个恢复配置只有backend.image变化；原镜像专用固定标签，durable override原子替换并fsync目录。仅backend重建，成功须有健康/TLS预期401、schema8/零rule/pilot、完整运行配置指纹、配对/暂停/账务旧行摘要及其他服务身份前后核对。任何失败保存报告，按当前schema选择正确回退并保留当前DB，不自动恢复旧库。

实际仅backend已切换为33d5f08默认构建，schema8、rules=0、pilots=0。健康和真实公网TLS匿名拒绝前后相同；完整运行配置指纹、所有旧表摘要/前6checksum、配对及暂停状态一致，frontend/PostgreSQL/Redis/New API的容器ID/镜像未变。未触发实际回退。

升级后Android签名健康首次失败，随后同网络栈匿名检查明确dns_failure；发现模拟器仍固定使用已不可达的旧网络LAN DNS，host当前DNS及公共DNS可响应。保留同一userdata/旧marker/端口映射，以已验证可达DNS冷启动；直接启动底层QEMU曾因SDK库路径失败，保留日志，改用SDK launcher成功。仅补受影响的实际HTTPS入口与既有凭据verify：401/JSON invalid_grant、AUTHORIZED/1份凭据/KeyStore不可导出、本地观察/事件/outbox均0、输入清理成功。没有重配或重跑生命周期/支付验收。签名health按既有协议更新device rate budget，后续严格全表审计正确提示该唯一变化；逐表复核确认只有`observer_device_budgets`变化，账务/配对/暂停未变，不把限流预算误当账务修改。旧失败目录保留。重启后原4笔合成账务/消费/收入/topup与总增额30,000,000仍不变，账务审计写新证据而不覆盖原报告。

这证明本次休眠升级与当前连接条件，不证明支付宝登录状态、自然后台可靠性、真实投递或付款归属。New API升级与SPEC-042普通消息对照仍待用户，真实模板/身份与金额政策仍独立；不补历史单、不要求再次付款、生产自动匹配不启用。
