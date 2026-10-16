# M3 New API 金额校验与受限回调基础

2026-10-16；#10/#27/#29，epay-go #8/#9；SPEC-023/024。证据为真实三数据库、签名HTTP handler与Go实际TLS。**尚未完成AVD→New API余额闭环**，不得据本报告关闭完整Goal或#27/#29。

## New API 本地补丁

基线`633b4c5b190dc1af99a2814391354d86948e7943`，本地提交`42c3424d3ce3b82d71668dba2b84f58315d7d078`，checkout `/tmp/epay-observer-new-api`。补丁`/tmp/epay-observer-newapi-artifacts/0001-epay-amount-validation.patch`，SHA-256 `9eed8453a796e4f2233b22bdd8f6ec9827eb5f2b32a41ace679254a5a94a65ff`。保留上游AGPL-3.0身份/许可证，不向上游推送，不复制进MIT实现。

签名覆盖的原money必填进入锁单事务；严格有界ASCII十进制转整数分，与原请求FormatFloat(Money,'f',2,64)比较。校验位于幂等成功之前；原额度计算、provider和状态、钱包上限、缓存与审计保留。历史2.675请求为2.67得到专门覆盖。

实际运行（Go1.26.9容器，无宿主工具安装）：

- `go test -race ./model -run '^TestRechargeEpay' -count=1 -v`：51项含子测试通过。
- `TEST_EPAY_DIALECT=<sqlite|postgres|mysql> go test -race ./controller -run '^TestEpayCallbackAmountMatrix$' -count=1 -v`：各14项通过。实际SQLite3.50.4、PostgreSQL16.15、MySQL8.4.11。
- GET/POST正确签名与金额、合法签名错误金额、缺省/非法精度、错误签名、未重签篡改、成功后的错金额、直接并发结算均覆盖。余额仅按原Amount增加一次。
- `go test -race ./model ./controller`：两包完整回归通过（14.778s/360.932s）；`go vet ./model ./controller`通过。完整包自带外部环境相关跳过不充当本批三数据库证据；定向矩阵没有跳过。
- 两轴复审均无遗留问题。原始日志含合成签名，仅保存在私有/tmp，不提交公开仓库。

## 网关受限本地TLS

基线`ccbaf7059710b6a38464c78c4a50e199ccd0462f`，功能/修复`842955b`、`f8ca3c2`。默认safehttp不变；本地client只在observersynthetic构建，要求显式环境、精确HTTPS数值环回URL/端口/固定路径和私有CA。禁止代理、DNS拨号、跳转及证书跳过。普通公网URL在事务外预校验；本地例外在锁定scope内再授权。投递前必须存在已提交claim/consumption/credited event及当前启用的synthetic范围。

- 最终默认/专用全仓race分别209/315项，0失败/跳过；两构建vet及专用服务二进制构建通过。
- 实际TLS成功；缺CA、错信任根、可信但错误IP SAN、HTTP、DNS名、私网非环回、query/fragment、编码路径、端口/配置不符、跳转拒绝。
- 两provider各自完成真实持久事件匹配/收入→TLS首次503→持久退避→停用测试merchant不发网络→重新启用后新worker成功；最终一条收入，成功后不自动重发。这些新worker实例测试不冒充进程崩溃。
- 普通paid订单无观察事件消费不能使用本地client；默认构建即使设置相同env仍拒绝环回。
- 复审修复公网DNS在支付锁内等待的回归；确定性阻塞resolver期间同通道其他订单成功，两构建race通过。两轴复审已清除问题。

前一匹配批次已合并：Android #55 `cfe5ff88fbc80811f05f2326bfc2bfb267740cb9`（CI38878342412）；epay-go #19 `ccbaf7059710b6a38464c78c4a50e199ccd0462f`（CI38878316247/38878305412）。

当前New API完整前后端已在专用本地容器启动，并通过普通setup和dashboard会话登录；下一批使用真实普通充值单/网关API、独立通知App、实际持久worker与回调故障，完成余额证据。所有账号、配置和CA为本任务隔离合成资源，不涉及真实支付兼容性。
