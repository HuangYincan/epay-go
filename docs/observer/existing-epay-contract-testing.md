# 既有EPay接口与通知准入分层验证

2026-10-19；Android #82 / epay-go #32，SPEC-046 EPI-01..04。仅修改网关与安卓观察器，New API线上与私有候选均未修改或部署。撤回其线上替换方案，已有私有研究/失败记录保留。

## 变更

operator严格输入v2将第五项改为merchant_callback_verified，核对既有商户接口而非要求其内部事务修复；事实仍需证据，工具不代批。Android严格v2 handoff只带四项通知事实。v1与混合字段拒绝，旧向量保留、新v2向量两端逐字相同。事件/签名/数据库schema和结算条件不变，无公网启用接口。尚未登记线上rule/pilot，没有真实订单或回调被本批处理。

## 实际验证

- 既有golang:1.26.9、postgres:16，独立无宿主端口网络/临时数据库，固定依赖离线readonly；原lab与所有线上服务不变。实际operator编译、受影响两包race通过132项测试/子测试（observerstore 128、CLI 4），0跳过，go vet通过。缺事实/旧版本在登记前拒绝、规则/试验/消费/收入零写入；原登记/恢复/并发/实际CLI私有输出条件回归通过。另补受影响operator在observersynthetic构建的15项race测试通过、0跳过。全部为合成数据。
- 使用既有SDK/JBR，离线严格依赖构建productionDebug/AndroidTest、productionRelease及fixtureDebug，JVM与production lint通过。production 47项中46通过、1既有私有研究默认跳过；fixture 156项中155通过、1同类默认跳过。新增版本/混合字段与四项review缺失/0反例均运行。
- 最终三个APK的productionRelease、productionDebug、fixtureDebug检查通过。没有安装本批APK、启用监听或重跑AVD/旧生命周期/支付验收，现有普通消息对照页保持WAITING。

私有证据：/private/tmp/observer-existing-epay-contract-20261019，含构建、JSON测试事件、APK检查和独立审查结果。独立Standards与Spec审查均无阻塞。Spec建议将数据库操作前拒绝明确限定为apply/登记路径，已同步双方规格；默认只读检查仍可读库报告阻塞。Android PR #95 / epay-go PR #33已创建，最终CI与合并状态以PR为准，不将本地结果冒充CI。

## 实际未验收

EPI-05真实通知、订单归属、商户回调和实际New API前后余额仍未完成。普通消息对照尚待用户本地开始；模板/聊天边界、通知身份及金额风险事实不自动变为1。商户先ACK和内部事务风险仍未修复，不能把success响应当实际增额或保证恰好一次；网关仅保证自身已核对的订单/账务和持久回调范围。旧订单不补确认、不手动回调、不再要求当前付款，生产自动匹配关闭。
