# Observer v1 protocol core

关联 HuangYincan/epay-go#6 与 HuangYincan/epay-observer-android#20/#25；SPEC-002/013。

只验证固定POST路由、单值headers、精确body HMAC、时间窗口和严格JSON。`VerifiedRequest`不是持久ACK，不校验数据库授权/撤销/环境，也不做重放登记、事件幂等、订单匹配或记账。尚未注册到任何HTTP路由。后续接收事务需要全部补齐后才能发送202/200回执。

黄金向量来自观察器仓库原有 `docs/testing/protocol-v1/vectors.json`，本批采用 `observer-events-v1-rc1`。这里复制的是本项目合成测试数据，不是第三方实现；key=00..1f为公开夹具，不是服务凭据。双方字节必须同步，不以重新计算新向量掩盖不一致。

宿主未找到Go工具链，未安装额外工具；使用仓库既有GitHub CI（go.mod固定版本、race、vet、build和隔离PostgreSQL测试）验证。没有启动或部署应用服务。测试结果以PR最终head为准；数据库接收/反重放/支付行为仍Not run。

2026-10-16：安全基线PR #12已合并（Go1.26.9/x/net0.60.0、Vue3.5.42/source-map-js1.2.2）。协议代码97a1927在CI run 38835577839通过Go/race/PostgreSQL既有测试、fuzz、vet、build、gofmt、govulncheck、前端审计构建及两个镜像构建；没有部署。Android代码PR #48复用同一向量，通过API37的实际JCA；数据库观察器事务仍未实现。
